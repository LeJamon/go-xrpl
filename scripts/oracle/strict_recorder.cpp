#include <test/jtx/Account.h>
#include <test/jtx/account_txn_id.h>
#include <test/jtx/delegate.h>
#include <test/jtx/Env.h>
#include <test/jtx/TestHelpers.h>
#include <test/jtx/amount.h>
#include <test/jtx/batch.h>
#include <test/jtx/envconfig.h>
#include <test/jtx/escrow.h>
#include <test/jtx/fee.h>
#include <test/jtx/flags.h>
#include <test/jtx/last_ledger_sequence.h>
#include <test/jtx/multisign.h>
#include <test/jtx/noop.h>
#include <test/jtx/offer.h>
#include <test/jtx/pay.h>
#include <test/jtx/regkey.h>
#include <test/jtx/seq.h>
#include <test/jtx/sig.h>
#include <test/jtx/sponsor.h>
#include <test/jtx/ticket.h>
#include <test/jtx/token.h>
#include <test/jtx/ter.h>
#include <test/jtx/txflags.h>
#include <test/jtx/trust.h>
#include <test/jtx/vault.h>

#include <xrpl/basics/strHex.h>
#include <xrpl/beast/unit_test/suite.h>
#include <xrpl/config/Constants.h>
#include <xrpl/core/NetworkIDService.h>
#include <xrpl/json/to_string.h>
#include <xrpl/ledger/AmendmentTable.h>
#include <xrpl/ledger/ApplyView.h>
#include <xrpl/ledger/Ledger.h>
#include <xrpl/ledger/LedgerTiming.h>
#include <xrpl/ledger/OpenView.h>
#include <xrpl/ledger/ReadView.h>
#include <xrpl/ledger/Sandbox.h>
#include <xrpl/protocol/Feature.h>
#include <xrpl/protocol/Indexes.h>
#include <xrpl/protocol/LedgerHeader.h>
#include <xrpl/protocol/MPTIssue.h>
#include <xrpl/protocol/Serializer.h>
#include <xrpl/protocol/SField.h>
#include <xrpl/protocol/SeqProxy.h>
#include <xrpl/protocol/STAmount.h>
#include <xrpl/protocol/STLedgerEntry.h>
#include <xrpl/protocol/STTx.h>
#include <xrpl/protocol/SystemParameters.h>
#include <xrpl/protocol/TER.h>
#include <xrpl/protocol/TxFormats.h>
#include <xrpl/protocol/TxFlags.h>
#include <xrpl/protocol/XRPAmount.h>
#include <xrpl/protocol/jss.h>
#include <xrpl/tx/apply.h>
#include <xrpld/app/ledger/LedgerMaster.h>
#include <xrpld/app/ledger/OpenLedger.h>
#include <xrpld/app/misc/TxQ.h>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <initializer_list>
#include <map>
#include <memory>
#include <optional>
#include <random>
#include <string>
#include <tuple>
#include <utility>
#include <vector>

namespace xrpl::test {

namespace {

using jtx::Account;
using jtx::Env;
using jtx::JTx;

constexpr char const* kFixtureVersion = "v4";
constexpr char const* kOracleRepository = "XRPLF/xrpld-private";
constexpr char const* kOracleTag = "3.4.1";
constexpr char const* kOracleCommit =
    "d147fccf54a500fce586522f28d6044c37fd8d29";

struct Profile
{
    bool cleanup;
    bool lending;
    bool batch;
    bool batchFix;

    [[nodiscard]] std::string
    name() const
    {
        return "c" + std::to_string(cleanup ? 1 : 0) + "-l" +
            std::to_string(lending ? 1 : 0) + "-b" +
            std::to_string(batch ? 1 : 0) + "-f" +
            std::to_string(batchFix ? 1 : 0);
    }

    [[nodiscard]] FeatureBitset
    features() const
    {
        auto features = test::jtx::testableAmendments();
        if (!cleanup)
            features = features - fixCleanup3_4_0;
        if (!lending)
            features = features - featureLendingProtocolV1_1;
        if (!batch)
            features = features - featureBatchV1_1;
        if (!batchFix)
            features = features - fixBatchV1_2;
        return features;
    }
};

struct Expected
{
    TER ter = tesSUCCESS;
    bool applied = true;
    bool queued = false;
};

std::uint32_t
networkSeconds(NetClock::time_point time)
{
    return time.time_since_epoch().count();
}

std::string
serialize(STTx const& transaction)
{
    Serializer raw;
    transaction.add(raw);
    return strHex(raw.slice());
}

json::Value
sleEntries(ReadView const& view)
{
    json::Value state{json::ValueType::Array};
    for (auto const& sle : view.sles)
    {
        if (!sle)
            continue;
        json::Value entry;
        Serializer raw;
        sle->add(raw);
        entry["index"] = to_string(sle->key());
        entry["data"] = strHex(raw.slice());
        state.append(entry);
    }
    return state;
}

using SerializedState = std::map<std::string, std::string>;

SerializedState
serializedState(ReadView const& view)
{
    SerializedState state;
    for (auto const& sle : view.sles)
    {
        if (!sle)
            continue;
        Serializer raw;
        sle->add(raw);
        state.emplace(to_string(sle->key()), strHex(raw.slice()));
    }
    return state;
}

struct OpenLedgerDelta
{
    json::Value inject{json::ValueType::Array};
    json::Value erase{json::ValueType::Array};
};

OpenLedgerDelta
openLedgerDelta(ReadView const& before, ReadView const& after)
{
    auto const oldState = serializedState(before);
    auto const newState = serializedState(after);
    OpenLedgerDelta delta;
    for (auto const& [index, oldData] : oldState)
    {
        auto const found = newState.find(index);
        if (found == newState.end() || found->second != oldData)
        {
            json::Value entry;
            entry["index"] = index;
            entry["data"] = oldData;
            delta.erase.append(entry);
        }
    }
    for (auto const& [index, newData] : newState)
    {
        auto const found = oldState.find(index);
        if (found == oldState.end() || found->second != newData)
        {
            json::Value entry;
            entry["index"] = index;
            entry["data"] = newData;
            delta.inject.append(entry);
        }
    }
    return delta;
}

std::optional<Keylet>
expiredOfferKey(ReadView const& view, Account const& owner)
{
    for (auto const& sle : view.sles)
    {
        if (sle && sle->getType() == ltOFFER &&
            sle->getAccountID(sfAccount) == owner.id() && sle->isFieldPresent(sfExpiration))
            return Keylet{ltOFFER, sle->key()};
    }
    return std::nullopt;
}

std::size_t
offerCount(ReadView const& view, Account const& owner)
{
    std::size_t count = 0;
    for (auto const& sle : view.sles)
    {
        if (sle && sle->getType() == ltOFFER && sle->getAccountID(sfAccount) == owner.id())
            ++count;
    }
    return count;
}

json::Value
snapshot(ReadView const& view)
{
    json::Value result;
    Serializer header;
    addRaw(view.header(), header, true);
    result["header"] = strHex(header.slice());

    result["rules"] = json::Value{json::ValueType::Array};
    foreachFeature(test::jtx::testableAmendments(), [&](uint256 const& id) {
        if (!view.rules().enabled(id))
            return;
        auto const idString = to_string(id);
        result["rules"].append(idString);
    });

    result["fees"]["base"] = to_string(view.fees().base);
    result["fees"]["reserve"] = to_string(view.fees().reserve);
    result["fees"]["increment"] = to_string(view.fees().increment);
    result["state"] = sleEntries(view);

    result["transactions"] = json::Value{json::ValueType::Array};
    for (auto const& [transaction, metadata] : view.txs)
    {
        if (!transaction)
            continue;
        json::Value entry;
        entry["hash"] = to_string(transaction->getTransactionID());
        entry["tx_blob"] = serialize(*transaction);
        if (metadata)
        {
            Serializer raw;
            metadata->add(raw);
            entry["meta_blob"] = strHex(raw.slice());
        }
        else
            entry["meta_blob"] = "";
        result["transactions"].append(entry);
    }
    return result;
}

json::Value
txqConfig()
{
    // These are the rippled defaults mirrored by the Go runner's txq.Config.
    json::Value result;
    result["ledgers_in_queue"] = 20;
    result["queue_size_min"] = 2000;
    result["retry_sequence_percent"] = 25;
    result["minimum_escalation_multiplier"] = 128000;
    result["minimum_txn_in_ledger"] = 32;
    result["minimum_txn_in_ledger_standalone"] = 1000;
    result["target_txn_in_ledger"] = 256;
    result["maximum_txn_in_ledger"] = 0;
    result["maximum_txn_in_ledger_set"] = false;
    result["normal_consensus_increase_percent"] = 20;
    result["slow_consensus_decrease_percent"] = 50;
    result["maximum_txn_per_account"] = 10;
    result["minimum_last_ledger_buffer"] = 2;
    result["standalone"] = true;
    return result;
}

json::Value
smallQueueTxqConfig()
{
    auto result = txqConfig();
    result["ledgers_in_queue"] = 2;
    result["queue_size_min"] = 2;
    result["minimum_txn_in_ledger_standalone"] = 2;
    result["normal_consensus_increase_percent"] = 0;
    return result;
}

std::unique_ptr<Config>
recorderConfig()
{
    return jtx::envconfig([](std::unique_ptr<Config> config) {
        auto& section = config->section(Sections::kTransactionQueue);
        section.set(Keys::kLedgersInQueue, "20");
        section.set(Keys::kMinimumQueueSize, "2000");
        section.set(Keys::kRetrySequencePercent, "25");
        section.set(Keys::kMinimumEscalationMultiplier, "128000");
        section.set(Keys::kMinimumTxnInLedger, "32");
        section.set(Keys::kMinimumTxnInLedgerStandalone, "1000");
        section.set(Keys::kTargetTxnInLedger, "256");
        section.set(Keys::kNormalConsensusIncreasePercent, "20");
        section.set(Keys::kSlowConsensusDecreasePercent, "50");
        section.set(Keys::kMaximumTxnPerAccount, "10");
        section.set(Keys::kMinimumLastLedgerBuffer, "2");
        return config;
    });
}

std::unique_ptr<Config>
smallQueueRecorderConfig()
{
    auto config = recorderConfig();
    auto& section = config->section(Sections::kTransactionQueue);
    section.set(Keys::kLedgersInQueue, "2");
    section.set(Keys::kMinimumQueueSize, "2");
    section.set(Keys::kMinLedgersToComputeSizeLimit, "3");
    section.set(Keys::kMaxLedgerCountsToStore, "100");
    section.set(Keys::kMinimumTxnInLedgerStandalone, "2");
    section.set(Keys::kNormalConsensusIncreasePercent, "0");
    return config;
}

void
configureServiceConfig(Config& config, Profile const& profile)
{
    config.startUp = StartUpType::Fresh;

    FeatureBitset forced;
    auto const profileFeatures = profile.features();
    foreachFeature(test::jtx::testableAmendments(), [&](uint256 const& feature) {
        auto const name = featureToName(feature);
        auto const amendment = allAmendments().find(name);
        if (amendment == allAmendments().end())
            return;

        if (amendment->second == AmendmentSupport::Retired)
        {
            if (profileFeatures[feature])
                forced.set(feature);
            return;
        }

        if (amendment->second != AmendmentSupport::Supported)
            return;

        auto& section = profileFeatures[feature]
            ? config.section(Sections::kAmendments)
            : config.section(Sections::kVetoAmendments);
        section.append(to_string(feature) + " " + name);
    });
    foreachFeature(forced, [&](uint256 const& feature) {
        config.features.insert(feature);
    });
}

void
assertPersistedAmendments(
    Env& env,
    Profile const& profile,
    beast::unit_test::Suite& suite)
{
    auto const amendments = env.closed()->read(keylet::amendments());
    if (!suite.expect(
            amendments != nullptr,
            "fresh parent did not persist Amendments singleton",
            __FILE__,
            __LINE__))
        return;

    std::vector<uint256> expected;
    foreachFeature(profile.features(), [&](uint256 const& feature) {
        auto const name = featureToName(feature);
        auto const amendment = allAmendments().find(name);
        if (amendment != allAmendments().end() &&
            amendment->second == AmendmentSupport::Supported)
            expected.push_back(feature);
    });
    std::ranges::sort(expected);

    auto actual = std::vector<uint256>{};
    if (amendments->isFieldPresent(sfAmendments))
    {
        auto const& persisted = amendments->getFieldV256(sfAmendments);
        actual.assign(persisted.begin(), persisted.end());
    }
    std::ranges::sort(actual);
    suite.expect(
        actual == expected,
        "persisted Amendments singleton did not match configured supported profile",
        __FILE__,
        __LINE__);
}

std::unique_ptr<Config>
networkConfig(std::uint32_t networkID)
{
    auto config = recorderConfig();
    config->networkId = networkID;
    return config;
}

void
assertFreshRuntime(Env& env, beast::unit_test::Suite& suite)
{
    auto const metrics = env.app().getTxQ().getMetrics(*env.current());
    suite.expect(
        metrics.txCount == 0,
        "transaction queue was not empty before recording",
        __FILE__,
        __LINE__);
    suite.expect(
        metrics.txInLedger == 0,
        "open ledger already contained transactions before recording",
        __FILE__,
        __LINE__);
    suite.expect(
        metrics.referenceFeeLevel == TxQ::kBaseLevel,
        "transaction queue reference fee level was not at its configured minimum",
        __FILE__,
        __LINE__);
    suite.expect(
        env.current()->fees().base.drops() == 10,
        "reference fee was not the configured unit-test minimum",
        __FILE__,
        __LINE__);
    suite.expect(
        env.current()->fees().reserve.drops() == 200'000'000,
        "account reserve was not the configured unit-test minimum",
        __FILE__,
        __LINE__);
    suite.expect(
        env.current()->fees().increment.drops() == 50'000'000,
        "owner reserve increment was not the configured unit-test minimum",
        __FILE__,
        __LINE__);
}

json::Value
transactionBlobs(ReadView const& view)
{
    json::Value result{json::ValueType::Array};
    for (auto const& [transaction, unused] : view.txs)
    {
        if (transaction)
            result.append(serialize(*transaction));
    }
    return result;
}

class FixtureRecorder
{
    beast::unit_test::Suite& suite_;
    std::filesystem::path directory_;

    static json::Value
    queueState(Env& env)
    {
        json::Value result;
        result["tx_blobs"] = json::Value{json::ValueType::Array};
        for (auto const& detail : env.app().getTxQ().getTxs())
        {
            if (detail.txn)
                result["tx_blobs"].append(serialize(*detail.txn));
        }

        auto const metrics = env.app().getTxQ().getMetrics(*env.current());
        result["metrics"]["tx_count"] = json::Value{static_cast<json::UInt>(metrics.txCount)};
        if (metrics.txQMaxSize)
            result["metrics"]["max_size"] = json::Value{static_cast<json::UInt>(*metrics.txQMaxSize)};
        else
            result["metrics"]["max_size"] = json::Value{json::ValueType::Null};
        result["metrics"]["tx_in_ledger"] = json::Value{static_cast<json::UInt>(metrics.txInLedger)};
        result["metrics"]["tx_per_ledger"] = json::Value{static_cast<json::UInt>(metrics.txPerLedger)};
        result["metrics"]["reference_fee_level"] = json::Value{static_cast<json::UInt>(metrics.referenceFeeLevel.value())};
        result["metrics"]["min_processing_fee_level"] = json::Value{static_cast<json::UInt>(metrics.minProcessingFeeLevel.value())};
        result["metrics"]["med_fee_level"] = json::Value{static_cast<json::UInt>(metrics.medFeeLevel.value())};
        result["metrics"]["open_ledger_fee_level"] = json::Value{static_cast<json::UInt>(metrics.openLedgerFeeLevel.value())};
        return result;
    }

    static json::Value
    submitBoundary(
        Env& env,
        json::Value const& response,
        JTx const& transaction,
        ReadView const& postSubmitView,
        bool captureQueue)
    {
        json::Value result;
        result["boundary"] = "open_ledger";
        auto const& rpcResult = response["result"];
        result["engine_result"] = rpcResult["engine_result"];
        result["engine_result_code"] = rpcResult["engine_result_code"];
        result["applied"] = rpcResult["applied"];
        result["queued"] = rpcResult["queued"];
        result["fee"] = rpcResult["applied"].asBool()
            ? transaction.jv[jss::Fee].asUInt()
            : 0;
        result["post_submit_sle"] = sleEntries(postSubmitView);
        if (captureQueue)
            result["queue"] = queueState(env);
        return result;
    }

public:
    FixtureRecorder(beast::unit_test::Suite& suite, std::filesystem::path directory)
        : suite_(suite), directory_(std::move(directory))
    {
        std::filesystem::create_directories(directory_);
    }

private:
    template <class PreBuilder, class Builder, class Mutator>
    void
    recordImpl(Profile const& profile,
               std::string const& family,
               std::string const& testcase,
               Env& env,
               PreBuilder&& preBuilder,
               Builder&& builder,
               Expected expected,
               json::Value txqConfigValue,
               bool includePreSubmit,
               bool captureQueue,
               Mutator&& mutator,
               std::vector<Expected> preExpected = {},
               json::Value history = {})
    {
        auto const parent = env.closed();
        auto cleanupKey = std::optional<Keylet>{};
        auto const isOfferCleanup =
            family == "OfferCreate" && testcase == "expired-offer-cleanup";
        if (family == "NFTokenAcceptOffer" && testcase == "expired-sell-offer-cleanup")
            cleanupKey = keylet::nftokenOffer(
                             Account{"minter"},
                             SeqProxy::rawSequence(env.seq(Account{"minter"}) - 1));
        else if (isOfferCleanup)
        {
            cleanupKey = expiredOfferKey(*parent, Account{"bob"});
            suite_.expect(
                cleanupKey.has_value(),
                testcase + " setup did not leave an expiring Offer",
                __FILE__,
                __LINE__);
        }

        if (cleanupKey)
            suite_.expect(
                parent->read(*cleanupKey) != nullptr,
                testcase + " cleanup target was not present in the captured parent",
                __FILE__,
                __LINE__);

        json::Value fixture;
        fixture["fixture_version"] = kFixtureVersion;
        fixture["oracle_repository"] = kOracleRepository;
        fixture["oracle_tag"] = kOracleTag;
        fixture["oracle_commit"] = kOracleCommit;
        fixture["suite"] = "app/StrictOracleRecorder";
        fixture["testcase"] = testcase;
        fixture["family"] = family;
        fixture["profile"] = profile.name();
        fixture["network_id"] = env.app().getNetworkIDService().getNetworkID();
        fixture["apply_flags"] = 0;
        fixture["skip_signature_verification"] = false;
        fixture["txq_config"] = std::move(txqConfigValue);
        fixture["parent"] = snapshot(*parent);
        if (history.size() != 0)
            fixture["history"] = std::move(history);

        if (includePreSubmit)
        {
            fixture["pre_submit"] = json::Value{json::ValueType::Array};
            auto const preTransactions = preBuilder(env);
            for (std::size_t preIndex = 0; preIndex < preTransactions.size(); ++preIndex)
            {
                auto const& preTransaction = preTransactions[preIndex];
                if (!suite_.expect(
                        preTransaction.stx != nullptr,
                        testcase + " pre-submit did not produce a signed STTx",
                        __FILE__,
                        __LINE__))
                    return;

                auto const txBlob = serialize(*preTransaction.stx);
                auto const response = env.rpc("submit", txBlob);
                auto const postSubmitView = env.current();
                json::Value entry;
                entry["tx_blob"] = txBlob;
                entry["submit"] =
                    submitBoundary(env, response, preTransaction, *postSubmitView, captureQueue);
                fixture["pre_submit"].append(entry);

                auto const parsed = Env::parseResult(response);
                auto const expectedPre = preIndex < preExpected.size()
                    ? preExpected[preIndex]
                    : Expected{};
                suite_.expect(
                    parsed.ter && *parsed.ter == expectedPre.ter,
                    testcase + " pre-submit returned an unexpected TER",
                    __FILE__,
                    __LINE__);
                suite_.expect(
                    response["result"]["applied"].asBool() == expectedPre.applied,
                    testcase + " pre-submit returned an unexpected applied flag",
                    __FILE__,
                    __LINE__);
                suite_.expect(
                    response["result"]["queued"].asBool() == expectedPre.queued,
                    testcase + " pre-submit returned an unexpected queued flag",
                    __FILE__,
                    __LINE__);
            }
        }

        auto const beforeTransient = env.current();
        if (!suite_.expect(
                mutator(env),
                testcase + " transient open-ledger mutation failed",
                __FILE__,
                __LINE__))
            return;
        auto const afterTransient = env.current();
        auto const delta = openLedgerDelta(*beforeTransient, *afterTransient);
        if (delta.inject.size() != 0)
            fixture["open_ledger_inject"] = delta.inject;
        if (delta.erase.size() != 0)
            fixture["open_ledger_erase"] = delta.erase;

        auto const transaction = builder(env);
        if (!suite_.expect(
                transaction.stx != nullptr,
                testcase + " did not produce a signed STTx",
                __FILE__,
                __LINE__))
            return;

        auto const txBlob = serialize(*transaction.stx);
        fixture["tx_blob"] = txBlob;

        auto const response = env.rpc("submit", txBlob);
        auto const postSubmitView = env.current();
        fixture["submit"] = submitBoundary(env, response, transaction, *postSubmitView, captureQueue);
        if (cleanupKey)
            suite_.expect(
                postSubmitView->read(*cleanupKey) == nullptr,
                testcase + " cleanup target remained in the open ledger after submit",
                __FILE__,
                __LINE__);
        if (isOfferCleanup)
            suite_.expect(
                offerCount(*postSubmitView, Account{"bob"}) == 1,
                testcase + " did not retain the unexpired Offer after cleanup",
                __FILE__,
                __LINE__);
        auto const parsed = Env::parseResult(response);
        suite_.expect(
            parsed.ter && *parsed.ter == expected.ter,
            testcase + " returned an unexpected TER",
            __FILE__,
            __LINE__);
        suite_.expect(
            response["result"]["applied"].asBool() == expected.applied,
            testcase + " returned an unexpected applied flag",
            __FILE__,
            __LINE__);
        suite_.expect(
            response["result"]["queued"].asBool() == expected.queued,
            testcase + " unexpectedly entered the transaction queue",
            __FILE__,
            __LINE__);

        auto const openBeforeClose = env.current();
        auto const requestedClose = env.now() + std::chrono::seconds{5};
        auto const& openHeader = openBeforeClose->header();
        auto const closeResolution = getNextLedgerTimeResolution(
            openHeader.closeTimeResolution,
            getCloseAgree(openHeader),
            openHeader.seq);
        auto const agreedCloseTime = openHeader.parentCloseTime + closeResolution;
        fixture["close_input"]["parent_close_time"] =
            networkSeconds(openHeader.parentCloseTime);
        fixture["close_input"]["close_time"] = networkSeconds(agreedCloseTime);
        fixture["close_input"]["ledger_sequence"] = openBeforeClose->header().seq;
        fixture["close_input"]["close_time_resolution"] = closeResolution.count();
        fixture["close_input"]["close_flags"] = openHeader.closeFlags;
        fixture["close_input"]["tx_blobs"] = transactionBlobs(*openBeforeClose);
        auto const closed = env.close(requestedClose);
        suite_.expect(closed, testcase + " ledger close failed", __FILE__, __LINE__);
        suite_.expect(
            env.closed()->header().closeTime == agreedCloseTime,
            testcase + " close time was not independently reproducible",
            __FILE__,
            __LINE__);
        if (cleanupKey)
            suite_.expect(
                env.closed()->read(*cleanupKey) == nullptr,
                testcase + " cleanup target remained in the closed ledger",
                __FILE__,
                __LINE__);
        if (isOfferCleanup)
            suite_.expect(
                offerCount(*env.closed(), Account{"bob"}) == 1,
                testcase + " did not retain the unexpired Offer in the closed ledger",
                __FILE__,
                __LINE__);
        fixture["closed"] = snapshot(*env.closed());

        auto const filename = profile.name() + "-" + family + "-" + testcase + ".json";
        std::ofstream output(directory_ / filename);
        output << to_string(fixture) << '\n';
        suite_.expect(output.good(), "unable to write " + filename, __FILE__, __LINE__);
    }

public:
    void
    record(Profile const& profile,
           std::string const& family,
           std::string const& testcase,
           Env& env,
           JTx const& transaction,
           std::optional<Expected> expected = std::nullopt)
    {
        Expected expectedResult = expected.value_or(Expected{});
        if (!expected)
        {
            expectedResult = {
                family == "Batch" && testcase == "poisoned-created-node-wrapper" && profile.batchFix
                    ? TER{temMALFORMED}
                    : TER{tesSUCCESS},
                family == "Batch" && testcase == "poisoned-created-node-wrapper" && profile.batchFix
                    ? false
                    : true,
                false};
        }
        auto noPreSubmit = [](Env&) { return std::vector<JTx>{}; };
        auto existingTransaction = [&transaction](Env&) { return transaction; };
        auto noTransient = [](Env&) { return true; };
        recordImpl(
            profile,
            family,
            testcase,
            env,
            noPreSubmit,
            existingTransaction,
            expectedResult,
            txqConfig(),
            false,
            false,
            noTransient);
    }

    template <class PreBuilder, class Builder>
    void
    recordWithHistory(Profile const& profile,
                      std::string const& family,
                      std::string const& testcase,
                      Env& env,
                      PreBuilder&& preBuilder,
                      Builder&& builder,
                      Expected expected)
    {
        recordImpl(
            profile,
            family,
            testcase,
            env,
            std::forward<PreBuilder>(preBuilder),
            std::forward<Builder>(builder),
            expected,
            smallQueueTxqConfig(),
            true,
            false,
            [](Env&) { return true; });
    }

    template <class PreBuilder, class Builder>
    void
    recordWithQueueCandidates(Profile const& profile,
                              std::string const& family,
                              std::string const& testcase,
                              Env& env,
                              PreBuilder&& preBuilder,
                              Builder&& builder,
                              std::vector<Expected> preExpected,
                              Expected expected,
                              json::Value history = {})
    {
        recordImpl(
            profile,
            family,
            testcase,
            env,
            std::forward<PreBuilder>(preBuilder),
            std::forward<Builder>(builder),
            expected,
            smallQueueTxqConfig(),
            true,
            true,
            [](Env&) { return true; },
            std::move(preExpected),
            std::move(history));
    }

    template <class Builder>
    void
    recordWithLedgerHistory(Profile const& profile,
                            std::string const& family,
                            std::string const& testcase,
                            Env& env,
                            json::Value history,
                            Builder&& builder,
                            Expected expected)
    {
        auto noPreSubmit = [](Env&) { return std::vector<JTx>{}; };
        recordImpl(
            profile,
            family,
            testcase,
            env,
            noPreSubmit,
            std::forward<Builder>(builder),
            expected,
            smallQueueTxqConfig(),
            false,
            true,
            [](Env&) { return true; },
            {},
            std::move(history));
    }

    template <class Mutator, class Builder>
    void
    recordWithTransient(Profile const& profile,
                        std::string const& family,
                        std::string const& testcase,
                        Env& env,
                        Mutator&& mutator,
                        Builder&& builder,
                        Expected expected)
    {
        auto noPreSubmit = [](Env&) { return std::vector<JTx>{}; };
        recordImpl(
            profile,
            family,
            testcase,
            env,
            noPreSubmit,
            std::forward<Builder>(builder),
            expected,
            txqConfig(),
            false,
            false,
            std::forward<Mutator>(mutator));
    }

    json::Value
    captureHistoryStep(Env& env, std::vector<JTx> const& transactions)
    {
        auto const parent = env.closed();
        json::Value step;
        step["parent"] = snapshot(*parent);
        step["pre_submit"] = json::Value{json::ValueType::Array};
        for (auto const& transaction : transactions)
        {
            if (!suite_.expect(
                    transaction.stx != nullptr,
                    "history pre-submit did not produce a signed STTx",
                    __FILE__,
                    __LINE__))
                return {};

            auto const txBlob = serialize(*transaction.stx);
            auto const response = env.rpc("submit", txBlob);
            auto const postSubmitView = env.current();
            json::Value entry;
            entry["tx_blob"] = txBlob;
            entry["submit"] = submitBoundary(env, response, transaction, *postSubmitView, true);
            step["pre_submit"].append(entry);
        }

        auto const openBeforeClose = env.current();
        auto const requestedClose = env.now() + std::chrono::seconds{5};
        auto const& openHeader = openBeforeClose->header();
        auto const closeResolution = getNextLedgerTimeResolution(
            openHeader.closeTimeResolution,
            getCloseAgree(openHeader),
            openHeader.seq);
        auto const agreedCloseTime = openHeader.parentCloseTime + closeResolution;
        step["close_input"]["parent_close_time"] = networkSeconds(openHeader.parentCloseTime);
        step["close_input"]["close_time"] = networkSeconds(agreedCloseTime);
        step["close_input"]["ledger_sequence"] = openHeader.seq;
        step["close_input"]["close_time_resolution"] = closeResolution.count();
        step["close_input"]["close_flags"] = openHeader.closeFlags;
        step["close_input"]["tx_blobs"] = transactionBlobs(*openBeforeClose);

        auto const closed = env.close(requestedClose);
        suite_.expect(closed, "history ledger close failed", __FILE__, __LINE__);
        suite_.expect(
            env.closed()->header().closeTime == agreedCloseTime,
            "history close time was not independently reproducible",
            __FILE__,
            __LINE__);
        step["closed"] = snapshot(*env.closed());
        step["queue"] = queueState(env);
        return step;
    }
};

JTx
makePayment(Env& env)
{
    return env.jt(jtx::pay(Account{"alice"}, Account{"bob"}, jtx::XRP(1)));
}

struct SeededPaymentSample
{
    std::uint32_t amount;
    std::uint32_t fee;
    std::uint32_t sequenceOffset;
    Expected expected;
    char const* label;
};

SeededPaymentSample
seededPaymentSample(std::uint32_t sample)
{
    std::mt19937 generator{2016};
    std::uint32_t amount = 1;
    for (std::uint32_t index = 0; index <= sample; ++index)
        amount = 1 + generator() % 8;

    switch (sample)
    {
        case 0:
            return {amount, 10, 0, Expected{TER{tesSUCCESS}, true, false}, "valid-base-fee"};
        case 1:
            return {
                9999,
                10,
                0,
                Expected{TER{tecUNFUNDED_PAYMENT}, true, false},
                "insufficient-balance"};
        case 2:
            return {amount, 11, 0, Expected{TER{tesSUCCESS}, true, false}, "valid-fee-edge"};
        case 3:
            return {
                amount,
                10,
                1,
                Expected{TER{terPRE_SEQ}, false, false},
                "future-sequence"};
        default:
            break;
    }

    return {amount, 10, 0, Expected{}, "invalid-sample"};
}

JTx
makeSeededPayment(Env& env, SeededPaymentSample const& sample)
{
    auto const alice = Account{"alice"};
    auto result = jtx::pay(alice, Account{"bob"}, jtx::XRP(sample.amount));
    if (sample.sequenceOffset != 0)
        result[jss::Sequence] = env.seq(alice) + sample.sequenceOffset;
    return env.jt(result, jtx::Fee(sample.fee));
}

std::vector<JTx>
makeQueuePrefill(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const aliceSequence = env.seq(alice);
    auto const bobSequence = env.seq(bob);
    std::vector<JTx> result;
    result.reserve(3);
    result.push_back(env.jt(
        jtx::pay(alice, bob, jtx::XRP(1000)),
        jtx::Seq(aliceSequence),
        jtx::Fee(10)));
    result.push_back(env.jt(
        jtx::pay(bob, alice, jtx::XRP(1)),
        jtx::Seq(bobSequence),
        jtx::Fee(10)));
    result.push_back(env.jt(
        jtx::pay(alice, bob, jtx::XRP(1)),
        jtx::Seq(aliceSequence + 1),
        jtx::Fee(10)));
    return result;
}

JTx
makeQueuedLowFee(Env& env)
{
    return env.jt(
        jtx::fset(Account{"alice"}, asfRequireDest),
        jtx::Fee(10));
}

void
fundAndClose(Env& env, std::initializer_list<Account> accounts);

void
setupQueueEviction(Env& env)
{
    auto fundPair = [&](Account const& first, Account const& second) {
        env.fund(jtx::XRP(10000), jtx::noripple(first));
        env.fund(jtx::XRP(10000), jtx::noripple(second));
        env.close();
    };
    fundPair(Account{"alice"}, Account{"bob"});
    fundPair(Account{"charlie"}, Account{"daria"});
    fundPair(Account{"erin"}, Account{"fred"});
    fundPair(Account{"gina"}, Account{"hank"});
}

std::vector<JTx>
makeQueueCandidates(Env& env)
{
    return {
        env.jt(jtx::fset(Account{"charlie"}, asfRequireDest), jtx::Fee(10)),
        env.jt(jtx::fset(Account{"daria"}, asfRequireDest), jtx::Fee(11)),
        env.jt(jtx::fset(Account{"erin"}, asfRequireDest), jtx::Fee(20)),
        env.jt(jtx::fset(Account{"fred"}, asfRequireDest), jtx::Fee(21)),
        env.jt(jtx::fset(Account{"gina"}, asfRequireDest), jtx::Fee(30)),
    };
}

std::vector<JTx>
makeQueueEvictionSubmissions(Env& env)
{
    auto result = makeQueuePrefill(env);
    auto candidates = makeQueueCandidates(env);
    result.insert(result.end(), candidates.begin(), candidates.end());
    return result;
}

JTx
makeQueueEvictionPrimary(Env& env)
{
    return env.jt(jtx::noop(Account{"hank"}), jtx::Fee(20000));
}

std::vector<JTx>
makeQueueHistoryFirstStep(Env& env)
{
    return makeQueueEvictionSubmissions(env);
}

std::vector<JTx>
makeQueueHistorySecondStep(Env&)
{
    return {};
}

JTx
makeQueueHistoryPrimary(Env& env)
{
    return env.jt(jtx::noop(Account{"charlie"}), jtx::Fee(10));
}

JTx
makeInvalidAccountSetFlags(Env& env)
{
    return env.jt(jtx::fset(Account{"alice"}, asfRequireDest, asfRequireDest));
}

JTx
makeNegativeFee(Env& env)
{
    return env.jt(jtx::noop(Account{"alice"}), jtx::Fee(1, true));
}

JTx
makeLowFee(Env& env)
{
    return env.jt(
        jtx::fset(Account{"alice"}, asfRequireDest),
        jtx::Fee(1));
}

JTx
makePastSequence(Env& env)
{
    auto const alice = Account{"alice"};
    auto result = jtx::noop(alice);
    result[jss::Sequence] = env.seq(alice) - 1;
    return env.jt(result);
}

JTx
makeFutureSequence(Env& env)
{
    auto const alice = Account{"alice"};
    return env.jt(jtx::noop(alice), jtx::Seq(env.seq(alice) + 1));
}

json::Value
networkTransaction(Env& env)
{
    auto const alice = Account{"alice"};
    json::Value result;
    result[jss::Account] = alice.human();
    result[jss::TransactionType] = jss::AccountSet;
    result[jss::Fee] = to_string(env.current()->fees().base);
    result[jss::Sequence] = env.seq(alice);
    return result;
}

JTx
makeNetworkMissing(Env& env)
{
    return env.jtnofill(networkTransaction(env));
}

JTx
makeNetworkWrong(Env& env)
{
    auto result = networkTransaction(env);
    result[jss::NetworkID] = 0;
    return env.jtnofill(result);
}

JTx
makeNonCanonicalNetworkID(Env& env)
{
    auto result = networkTransaction(env);
    result[jss::NetworkID] = 0;
    return env.jtnofill(result);
}

JTx
makeExpiredLedger(Env& env)
{
    return env.jt(
        jtx::noop(Account{"alice"}),
        jtx::LastLedgerSeq(env.current()->header().seq - 1));
}

JTx
makeWrongPrior(Env& env)
{
    return env.jt(jtx::noop(Account{"alice"}), jtx::AccountTxnId(uint256(1)));
}

JTx
makeMissingTicket(Env& env)
{
    auto const alice = Account{"alice"};
    return env.jt(jtx::noop(alice), jtx::ticket::Use(env.seq(alice) + 1));
}

JTx
makeValidTicket(Env& env)
{
    auto const alice = Account{"alice"};
    return env.jt(jtx::noop(alice), jtx::ticket::Use(env.seq(alice) - 1));
}

JTx
makeConsumedTicket(Env& env)
{
    auto const alice = Account{"alice"};
    return env.jt(jtx::noop(alice), jtx::ticket::Use(env.seq(alice) - 1));
}

JTx
makeRegularKeyPayment(Env& env)
{
    return env.jt(jtx::noop(Account{"alice"}), jtx::Sig(Account{"bob"}));
}

JTx
makeMasterDisabled(Env& env)
{
    return env.jt(jtx::noop(Account{"alice"}), jtx::Sig(Account{"alice"}));
}

JTx
makeWrongKey(Env& env)
{
    return env.jt(jtx::noop(Account{"alice"}), jtx::Sig(Account{"bob"}));
}

JTx
makeMissingMultisignQuorum(Env& env)
{
    return env.jt(
        jtx::noop(Account{"alice"}),
        jtx::Fee(20),
        jtx::Msig(Account{"bob"}));
}

JTx
makeValidMultisign(Env& env)
{
    return env.jt(
        jtx::noop(Account{"alice"}),
        jtx::Fee(30),
        jtx::Msig(Account{"bob"}, Account{"carol"}));
}

JTx
makeDelegatePermissionDenied(Env& env)
{
    return env.jt(
        jtx::pay(Account{"alice"}, Account{"carol"}, jtx::XRP(1)),
        jtx::delegate::As(Account{"carol"}));
}

JTx
makeDelegateSponsorPayment(Env& env)
{
    return env.jt(
        jtx::pay(Account{"alice"}, Account{"carol"}, jtx::XRP(100)),
        jtx::delegate::As(Account{"bob"}),
        jtx::Fee(jtx::XRP(10)),
        jtx::sponsor::As(Account{"sponsor"}, spfSponsorFee),
        jtx::Sig(sfSponsorSignature, Account{"sponsor"}));
}

JTx
makeFirstError(Env& env)
{
    auto result = jtx::fset(Account{"alice"}, asfRequireDest, asfRequireDest);
    return env.jt(result, jtx::Fee(1, true));
}

JTx
makeAccountSet(Env& env)
{
    return env.jt(jtx::fset(Account{"alice"}, asfRequireDest));
}

JTx
makeTrustSet(Env& env)
{
    return env.jt(jtx::trust(Account{"alice"}, Account{"gateway"}["USD"](100)));
}

JTx
makeTicketCreate(Env& env)
{
    return env.jt(jtx::ticket::create(Account{"alice"}, 1));
}

std::uint32_t
parentCloseTime(Env& env)
{
    return env.current()->header().parentCloseTime.time_since_epoch().count();
}

JTx
makeExpiredNFTokenAccept(Env& env)
{
    auto const minter = Account{"minter"};
    auto const buyer = Account{"buyer"};
    auto const offer = keylet::nftokenOffer(
                           minter,
                           SeqProxy::rawSequence(env.seq(minter) - 1))
                           .key;
    return env.jt(jtx::token::acceptSellOffer(buyer, offer));
}

std::tuple<uint256, uint256>
mintAndOfferNFT(Env& env, Account const& account, jtx::PrettyAmount const& currency, std::uint32_t xfee = 0)
{
    auto const nftID = jtx::token::getNextID(env, account, 0, tfTransferable, xfee);
    env(jtx::token::mint(account, 0), jtx::token::XferFee(xfee), jtx::Txflags(tfTransferable));
    env.close();

    auto const sellIdx = keylet::nftokenOffer(
                             account,
                             SeqProxy::rawSequence(env.seq(account)))
                             .key;
    env(jtx::token::createOffer(account, nftID, currency), jtx::Txflags(tfSellNFToken));
    env.close();
    return {nftID, sellIdx};
}

JTx
makeExpiredOfferFillOrKill(Env& env)
{
    auto const alice = Account{"alice"};
    auto const gateway = Account{"gateway"};
    return env.jt(
        jtx::offer(alice, jtx::XRP(1000), gateway["USD"](1000)),
        jtx::Txflags(tfFillOrKill));
}

JTx
makeBatch(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const carol = Account{"carol"};
    auto const sequence = env.seq(alice);
    auto const fee = jtx::batch::calcBatchFee(env, 2, 2);
    return env.jt(
        jtx::batch::outer(alice, sequence, fee, tfAllOrNothing),
        jtx::batch::Inner(jtx::pay(bob, alice, jtx::XRP(1)), env.seq(bob)),
        jtx::batch::Inner(jtx::pay(carol, alice, jtx::XRP(1)), env.seq(carol)),
        jtx::batch::Sig(bob, carol));
}

JTx
makeBatchOnlyOneMixed(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const sequence = env.seq(alice);
    auto const fee = jtx::batch::calcBatchFee(env, 0, 3);
    return env.jt(
        jtx::batch::outer(alice, sequence, fee, tfOnlyOne),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(9999)), sequence + 1),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(1)), sequence + 2),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(2)), sequence + 3));
}

JTx
makeBatchUntilFailure(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const sequence = env.seq(alice);
    auto const fee = jtx::batch::calcBatchFee(env, 0, 3);
    return env.jt(
        jtx::batch::outer(alice, sequence, fee, tfUntilFailure),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(1)), sequence + 1),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(9999)), sequence + 2),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(2)), sequence + 3));
}

JTx
makeBatchRollback(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const sequence = env.seq(alice);
    auto const fee = jtx::batch::calcBatchFee(env, 0, 2);
    return env.jt(
        jtx::batch::outer(alice, sequence, fee, tfAllOrNothing),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(1)), sequence + 1),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(9999)), sequence + 2));
}

JTx
makeBatchIndependentMixed(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const sequence = env.seq(alice);
    auto const fee = jtx::batch::calcBatchFee(env, 0, 4);
    return env.jt(
        jtx::batch::outer(alice, sequence, fee, tfIndependent),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(1)), sequence + 1),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(9999)), sequence + 2),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(9999)), sequence + 3),
        jtx::batch::Inner(jtx::pay(alice, bob, jtx::XRP(3)), sequence + 4));
}

JTx
makePoisonedBatch(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const sequence = env.seq(alice);
    auto const fee = jtx::batch::calcBatchFee(env, 0, 2);
    auto wrap = [&](std::uint32_t innerSequence) {
        auto inner = jtx::pay(alice, bob, jtx::XRP(1));
        inner[jss::SigningPubKey] = "";
        inner[jss::Sequence] = innerSequence;
        inner[jss::Fee] = "0";
        inner[jss::Flags] = tfInnerBatchTxn;
        json::Value wrapped;
        wrapped[sfCreatedNode.jsonName] = inner;
        return wrapped;
    };
    auto result = jtx::batch::outer(alice, sequence, fee, tfAllOrNothing);
    result[jss::RawTransactions][0u] = wrap(sequence + 1);
    result[jss::RawTransactions][1u] = wrap(sequence + 2);
    return env.jt(result);
}

JTx
makeVaultCreate(Env& env)
{
    auto const owner = Account{"alice"};
    jtx::Vault vault{env};
    auto const result = vault.create({.owner = owner, .asset = xrpIssue()});
    return env.jt(std::get<0>(result));
}

JTx
makeLoanBrokerSet(Env& env)
{
    auto const owner = Account{"alice"};
    jtx::Vault vault{env};
    auto const [create, keylet, unusedSubscription] = vault.createClosedEnded(
        {.owner = owner,
         .asset = xrpIssue(),
         .subscriptionOffset = std::chrono::seconds{10},
         .investmentWindow = std::chrono::seconds{1'000'000}});
    env(create);
    auto const closeSucceeded = env.close();
    env.test.expect(
        closeSucceeded,
        "closed-ended vault prerequisite failed",
        __FILE__,
        __LINE__);
    return env.jt(jtx::loan_broker::set(owner.id(), keylet.key));
}

void
fundAndClose(Env& env, std::initializer_list<Account> accounts)
{
    for (auto const& account : accounts)
        env.fund(jtx::XRP(10000), account);
    env.close();
}

std::unique_ptr<Config>
serviceConfig(Profile const& profile)
{
    auto config = recorderConfig();
    configureServiceConfig(*config, profile);
    return config;
}

void
setupPastSequence(Env& env)
{
    auto const alice = Account{"alice"};
    fundAndClose(env, {alice});
    env(jtx::fset(alice, asfRequireDest));
    env.close();
}

void
setupExpiredLedger(Env& env)
{
    fundAndClose(env, {Account{"alice"}});
}

void
setupPriorConstraint(Env& env)
{
    auto const alice = Account{"alice"};
    fundAndClose(env, {alice});
    env(jtx::fset(alice, asfAccountTxnID));
    env.close();
}

void
setupExpiredNFTokenOffer(Env& env)
{
    auto const issuer = Account{"issuer"};
    auto const minter = Account{"minter"};
    auto const buyer = Account{"buyer"};
    fundAndClose(env, {issuer, minter, buyer});
    env(jtx::token::setMinter(issuer, minter));
    env.close();

    auto const nft = jtx::token::getNextID(env, issuer, 0, tfTransferable);
    env(
        jtx::token::mint(minter, 0),
        jtx::token::Issuer(issuer),
        jtx::Txflags(tfTransferable));
    env.close();

    auto const expiration = parentCloseTime(env) + 25;
    env(
        jtx::token::createOffer(minter, nft, jtx::drops(1)),
        jtx::token::Expiration(expiration),
        jtx::Txflags(tfSellNFToken));
    env.close();
    while (parentCloseTime(env) < expiration)
        env.close();
}

void
setupExpiredOffer(Env& env)
{
    auto const gateway = Account{"gateway"};
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    fundAndClose(env, {gateway, alice, bob});

    auto expired = jtx::offer(bob, gateway["USD"](500), jtx::XRP(500));
    expired[sfExpiration.jsonName] = parentCloseTime(env) + 1;
    env(expired);
    env.close();

    auto const expiredKey = expiredOfferKey(*env.current(), bob);
    env.test.expect(
        expiredKey.has_value(),
        "expired Offer setup did not create an Offer entry",
        __FILE__,
        __LINE__);

    env(jtx::offer(bob, gateway["USD"](500), jtx::XRP(500)));
    env.close();
    env(jtx::trust(alice, gateway["USD"](1000)));
    env.close();
    env(jtx::pay(gateway, alice, gateway["USD"](1000)));
    env.close();
}

void
setupValidTicket(Env& env)
{
    auto const alice = Account{"alice"};
    fundAndClose(env, {alice});
    env(jtx::ticket::create(alice, 1));
    env.close();
}

void
setupConsumedTicket(Env& env)
{
    auto const alice = Account{"alice"};
    fundAndClose(env, {alice});
    env(jtx::ticket::create(alice, 2));
    env.close();
    env(jtx::noop(alice), jtx::ticket::Use(env.seq(alice) - 1));
    env.close();
}

void
setupTicketReserveFailure(Env& env)
{
    auto const alice = Account{"alice"};
    env.fund(env.current()->fees().accountReserve(1, 1) - jtx::drops(1), alice);
    env.close();
}

void
setupRegularKey(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    fundAndClose(env, {alice, bob});
    env(jtx::regkey(alice, bob));
    env.close();
}

void
setupMasterDisabled(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    fundAndClose(env, {alice, bob});
    env(jtx::regkey(alice, bob));
    env.close();
    env(jtx::fset(alice, asfDisableMaster), jtx::Sig(alice));
    env.close();
}

void
setupMultisign(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const carol = Account{"carol"};
    fundAndClose(env, {alice, bob, carol});
    env(jtx::signers(alice, 2, {{bob, 1}, {carol, 1}}));
    env.close();
}

void
setupDelegate(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const carol = Account{"carol"};
    fundAndClose(env, {alice, bob, carol});
    env(jtx::delegate::set(alice, bob, {"Payment"}));
    env.close();
}

void
setupDelegateSponsor(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    auto const carol = Account{"carol"};
    auto const sponsor = Account{"sponsor"};
    fundAndClose(env, {alice, bob, carol, sponsor});
    env(jtx::delegate::set(alice, bob, {"Payment"}));
    env.close();
}

template <class Mutator>
void
installMalformedParent(Env& env, Mutator&& mutator)
{
    auto const base = std::dynamic_pointer_cast<Ledger const>(env.closed());
    env.test.expect(
        base != nullptr,
        "closed ledger was not a Ledger",
        __FILE__,
        __LINE__);
    if (!base)
        return;

    auto malformed = std::make_shared<Ledger>(
        *base, base->header().closeTime + base->header().closeTimeResolution);
    {
        OpenView view(malformed.get());
        if (!mutator(view))
        {
            env.test.expect(false, "failed to locate malformed parent entry", __FILE__, __LINE__);
            return;
        }
        view.apply(*malformed);
    }
    malformed->updateSkipList();
    malformed->setAccepted(
        malformed->header().closeTime,
        malformed->header().closeTimeResolution,
        true);

    auto retries = OrderedTxs({});
    env.app().getOpenLedger().accept(
        env.app(),
        malformed->rules(),
        malformed,
        OrderedTxs({}),
        false,
        retries,
        TapNone);
    env.app().getLedgerMaster().switchLCL(malformed);
    env.timeKeeper().set(malformed->header().closeTime);
}

void
setupMalformedEscrow(Env& env)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    fundAndClose(env, {alice, bob});

    auto const escrowSequence = env.seq(alice);
    env(
        jtx::escrow::create(alice, bob, jtx::XRP(1)),
        jtx::escrow::kFinishTime(env.now() + std::chrono::seconds{1}),
        jtx::escrow::kCancelTime(env.now() + std::chrono::seconds{2}));
    env.test.expect(env.close(), "escrow setup ledger close failed", __FILE__, __LINE__);

    auto const escrowKey =
        keylet::escrow(alice.id(), SeqProxy::rawSequence(escrowSequence));
    auto const malformedAmount = kInitialXrp;
    auto const fee = env.closed()->fees().base;
    installMalformedParent(env, [&](OpenView& view) {
        auto const escrow = view.read(escrowKey);
        auto const owner = view.read(keylet::account(alice.id()));
        if (!escrow || !owner)
            return false;
        auto replacement = std::make_shared<SLE>(*escrow);
        replacement->setFieldAmount(sfAmount, malformedAmount);
        view.rawReplace(replacement);
        auto ownerReplacement = std::make_shared<SLE>(*owner);
        ownerReplacement->setFieldAmount(sfBalance, fee);
        view.rawReplace(ownerReplacement);
        return true;
    });

    auto const escrow = env.le(escrowKey);
    env.test.expect(
        escrow && escrow->getFieldAmount(sfAmount) == malformedAmount,
        "malformed escrow amount was not preserved in the parent ledger",
        __FILE__,
        __LINE__);
}

void
setupFatalInvariantAccount(Env& env)
{
    auto const alice = Account{"alice"};
    auto const sponsorAccount = Account{"sponsor"};
    fundAndClose(env, {alice, sponsorAccount});
    env(
        jtx::sponsor::set_fee(sponsorAccount, 0, jtx::drops(100)),
        jtx::sponsor::SponseeAcc(alice));
    env.close();
    installMalformedParent(env, [&](OpenView& view) {
        auto const owner = view.read(keylet::account(alice.id()));
        if (!owner)
            return false;
        auto replacement = std::make_shared<SLE>(*owner);
        replacement->setFieldAmount(sfBalance, jtx::drops(-1));
        view.rawReplace(replacement);
        return true;
    });

    auto const owner = env.le(keylet::account(alice.id()));
    env.test.expect(
        owner && owner->getFieldAmount(sfBalance).xrp() < XRPAmount{0},
        "fatal invariant setup did not preserve an invalid account balance",
        __FILE__,
        __LINE__);
}

JTx
makeFatalInvariantRecovery(Env& env)
{
    return env.jt(
        jtx::fset(Account{"alice"}, asfRequireDest),
        jtx::sponsor::As(Account{"sponsor"}, spfSponsorFee));
}

JTx
makeExpiredEscrowCancel(Env& env)
{
    auto const alice = Account{"alice"};
    return env.jt(jtx::escrow::cancel(alice, alice, env.seq(alice) - 1));
}

bool
insertUnauthorizedNFTrustline(Env& env)
{
    auto const gateway = Account{"G1"};
    auto const buyer = Account{"A1"};
    return env.app().getOpenLedger().modify([&](OpenView& view, beast::Journal) {
        auto const trustline = std::make_shared<SLE>(
            keylet::trustLine(buyer, gateway, gateway["USD"].currency));
        trustline->setFieldAmount(sfBalance, gateway["USD"](-1000));
        view.rawInsert(trustline);
        return true;
    });
}

void
setupUnauthorizedCreateBuyOffer(Env& env, uint256& nftID)
{
    auto const gateway = Account{"G1"};
    auto const buyer = Account{"A1"};
    auto const seller = Account{"A2"};
    fundAndClose(env, {gateway, buyer, seller});
    env(jtx::fset(gateway, asfRequireAuth));
    env.close();

    auto const minted = mintAndOfferNFT(env, seller, jtx::drops(1));
    nftID = std::get<0>(minted);
    auto unfundedOffer =
        jtx::token::createOffer(buyer, nftID, gateway["USD"](10));
    env(unfundedOffer, jtx::token::Owner(seller), jtx::Ter(tecUNFUNDED_OFFER));
    env.close();
}

JTx
makeUnauthorizedCreateBuyOffer(Env& env, uint256 const& nftID)
{
    auto const gateway = Account{"G1"};
    auto const buyer = Account{"A1"};
    auto const seller = Account{"A2"};
    return env.jt(
        jtx::token::createOffer(buyer, nftID, gateway["USD"](10)),
        jtx::token::Owner(seller));
}

void
setupUnauthorizedAcceptBuyOffer(Env& env, uint256& buyIdx)
{
    auto const gateway = Account{"G1"};
    auto const buyer = Account{"A1"};
    auto const seller = Account{"A2"};
    auto const usd = gateway["USD"];
    fundAndClose(env, {gateway, buyer, seller});
    env(jtx::fset(gateway, asfRequireAuth));
    env.close();

    auto const limit = usd(10000);
    auto const [nftID, unusedSellIdx] = mintAndOfferNFT(env, seller, jtx::drops(1));
    (void)unusedSellIdx;
    env(jtx::trust(buyer, limit));
    env(jtx::trust(gateway, limit, buyer, tfSetfAuth));
    env(jtx::pay(gateway, buyer, usd(10)));
    env(jtx::trust(seller, limit));
    env(jtx::trust(gateway, limit, seller, tfSetfAuth));
    env(jtx::pay(gateway, seller, usd(10)));
    env.close();

    buyIdx = keylet::nftokenOffer(buyer, SeqProxy::rawSequence(env.seq(buyer))).key;
    env(jtx::token::createOffer(buyer, nftID, usd(10)), jtx::token::Owner(seller));
    env.close();
    env(jtx::pay(buyer, gateway, usd(10)));
    env(jtx::trust(buyer, usd(0)));
    env(jtx::trust(gateway, buyer["USD"](0)));
    env.close();
}

JTx
makeUnauthorizedAcceptBuyOffer(Env& env, uint256 const& buyIdx)
{
    return env.jt(jtx::token::acceptBuyOffer(Account{"A2"}, buyIdx));
}

void
setupUnauthorizedAcceptSellOffer(Env& env, uint256& sellIdx)
{
    auto const gateway = Account{"G1"};
    auto const buyer = Account{"A1"};
    auto const seller = Account{"A2"};
    auto const usd = gateway["USD"];
    fundAndClose(env, {gateway, buyer, seller});
    env(jtx::fset(gateway, asfRequireAuth));
    env.close();
    auto const limit = usd(10000);
    env(jtx::trust(seller, limit));
    env(jtx::trust(gateway, limit, seller, tfSetfAuth));
    auto const [unusedNftID, offer] = mintAndOfferNFT(env, seller, usd(10));
    (void)unusedNftID;
    sellIdx = offer;
    auto insufficientFunds = jtx::token::acceptSellOffer(buyer, sellIdx);
    env(insufficientFunds, jtx::Ter(tecINSUFFICIENT_FUNDS));
    env.close();
}

JTx
makeUnauthorizedAcceptSellOffer(Env& env, uint256 const& sellIdx)
{
    return env.jt(jtx::token::acceptSellOffer(Account{"A1"}, sellIdx));
}

void
setupUnauthorizedBrokerOffer(Env& env, uint256& buyIdx, uint256& sellIdx)
{
    auto const gateway = Account{"G1"};
    auto const buyer = Account{"A1"};
    auto const seller = Account{"A2"};
    auto const broker = Account{"broker"};
    auto const usd = gateway["USD"];
    fundAndClose(env, {gateway, buyer, seller, broker});
    env(jtx::fset(gateway, asfRequireAuth));
    env.close();
    auto const limit = usd(10000);
    env(jtx::trust(buyer, limit));
    env(jtx::trust(gateway, usd(0), buyer, tfSetfAuth));
    env(jtx::pay(gateway, buyer, usd(1000)));
    env(jtx::trust(seller, limit));
    env(jtx::trust(gateway, usd(0), seller, tfSetfAuth));
    env(jtx::pay(gateway, seller, usd(1000)));
    env(jtx::trust(broker, limit));
    env(jtx::trust(gateway, usd(0), broker, tfSetfAuth));
    env(jtx::pay(gateway, broker, usd(1000)));
    env.close();

    auto const [nftID, sellOffer] = mintAndOfferNFT(env, seller, usd(10));
    sellIdx = sellOffer;
    buyIdx = keylet::nftokenOffer(buyer, SeqProxy::rawSequence(env.seq(buyer))).key;
    env(jtx::token::createOffer(buyer, nftID, usd(11)), jtx::token::Owner(seller));
    env.close();
    env(jtx::pay(buyer, gateway, usd(1000)));
    env(jtx::trust(buyer, usd(0)));
    env.close();
}

JTx
makeUnauthorizedBrokerOffer(Env& env, uint256 const& buyIdx, uint256 const& sellIdx)
{
    auto const gateway = Account{"G1"};
    return env.jt(
        jtx::token::brokerOffers(Account{"broker"}, buyIdx, sellIdx),
        jtx::token::BrokerFee(gateway["USD"](1)));
}

bool
insertMissingMPTEscrow(Env& env, std::uint32_t sequence)
{
    auto const alice = Account{"alice"};
    auto const bob = Account{"bob"};
    return env.app().getOpenLedger().modify([&](OpenView& view, beast::Journal) {
        Sandbox sandbox{&view, TapNone};
        auto const escrow = std::make_shared<SLE>(
            keylet::escrow(alice, SeqProxy::rawSequence(sequence)));
        MPTIssue const issue{makeMptID(1, AccountID(0x4985601))};
        STAmount const amount{issue, 10};
        escrow->setAccountID(sfDestination, bob);
        escrow->setFieldAmount(sfAmount, amount);
        sandbox.insert(escrow);
        sandbox.apply(view);
        return true;
    });
}

JTx
makeMPTFinishPreclaim(Env& env, std::uint32_t sequence)
{
    auto const baseFee = env.current()->fees().base;
    return env.jt(
        jtx::escrow::finish(Account{"bob"}, Account{"alice"}, sequence),
        jtx::escrow::kCondition(jtx::escrow::kCb1),
        jtx::escrow::kFulfillment(jtx::escrow::kFb1),
        jtx::Fee(baseFee * 150));
}

JTx
makeMPTCancelPreclaim(Env& env, std::uint32_t sequence)
{
    return env.jt(
        jtx::escrow::cancel(Account{"bob"}, Account{"alice"}, sequence),
        jtx::Fee(env.current()->fees().base));
}

template <class Builder>
void
recordWithSetup(FixtureRecorder& recorder,
                beast::unit_test::Suite& suite,
                Profile const& profile,
                std::string const& family,
                 std::string const& testcase,
                 std::initializer_list<Account> accounts,
                 Builder&& builder,
                 std::optional<Expected> expected = std::nullopt)
{
    auto config = recorderConfig();
    configureServiceConfig(*config, profile);
    Env env{suite, std::move(config), FeatureBitset{}};
    env.app().checkSigs(true);
    fundAndClose(env, accounts);
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);
    recorder.record(profile, family, testcase, env, builder(env), expected);
}

template <class Setup, class Builder>
void
recordScenario(FixtureRecorder& recorder,
               beast::unit_test::Suite& suite,
               Profile const& profile,
               std::string const& family,
               std::string const& testcase,
               std::unique_ptr<Config> config,
               Setup&& setup,
               Builder&& builder,
               Expected expected)
{
    configureServiceConfig(*config, profile);
    Env env{suite, std::move(config), FeatureBitset{}};
    env.app().checkSigs(true);
    setup(env);
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);
    recorder.record(profile, family, testcase, env, builder(env), expected);
}

template <class Setup, class Mutator, class Builder>
void
recordTransientScenario(FixtureRecorder& recorder,
                        beast::unit_test::Suite& suite,
                        Profile const& profile,
                        std::string const& family,
                        std::string const& testcase,
                        Setup&& setup,
                        Mutator&& mutator,
                        Builder&& builder,
                        Expected expected)
{
    auto config = recorderConfig();
    configureServiceConfig(*config, profile);
    Env env{suite, std::move(config), FeatureBitset{}};
    env.app().checkSigs(true);
    setup(env);
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);
    recorder.recordWithTransient(
        profile,
        family,
        testcase,
        env,
        std::forward<Mutator>(mutator),
        std::forward<Builder>(builder),
        expected);
}

void
recordNFTokenAuthTransientScenarios(
    FixtureRecorder& recorder,
    beast::unit_test::Suite& suite,
    Profile const& profile)
{
    {
        uint256 nftID;
        recordTransientScenario(
            recorder,
            suite,
            profile,
            "NFTokenAuth",
            "Unauthorized_buyer_tries_to_create_buy_offer",
            [&](Env& env) { setupUnauthorizedCreateBuyOffer(env, nftID); },
            insertUnauthorizedNFTrustline,
            [&](Env& env) { return makeUnauthorizedCreateBuyOffer(env, nftID); },
            Expected{TER{tecNO_AUTH}, true, false});
    }
    {
        uint256 buyIdx;
        recordTransientScenario(
            recorder,
            suite,
            profile,
            "NFTokenAuth",
            "Seller_tries_to_accept_buy_offer_from_unauth_buyer",
            [&](Env& env) { setupUnauthorizedAcceptBuyOffer(env, buyIdx); },
            insertUnauthorizedNFTrustline,
            [&](Env& env) { return makeUnauthorizedAcceptBuyOffer(env, buyIdx); },
            Expected{TER{tecNO_AUTH}, true, false});
    }
    {
        uint256 sellIdx;
        recordTransientScenario(
            recorder,
            suite,
            profile,
            "NFTokenAuth",
            "Unauthorized_buyer_tries_to_accept_sell_offer",
            [&](Env& env) { setupUnauthorizedAcceptSellOffer(env, sellIdx); },
            insertUnauthorizedNFTrustline,
            [&](Env& env) { return makeUnauthorizedAcceptSellOffer(env, sellIdx); },
            Expected{TER{tecNO_AUTH}, true, false});
    }
    {
        uint256 buyIdx;
        uint256 sellIdx;
        recordTransientScenario(
            recorder,
            suite,
            profile,
            "NFTokenAuth",
            "Authorized_broker_tries_to_bridge_offers_from_unauthorized_buyer.",
            [&](Env& env) { setupUnauthorizedBrokerOffer(env, buyIdx, sellIdx); },
            insertUnauthorizedNFTrustline,
            [&](Env& env) { return makeUnauthorizedBrokerOffer(env, buyIdx, sellIdx); },
            Expected{TER{tecNO_AUTH}, true, false});
    }
}

void
recordEscrowTokenTransientScenarios(
    FixtureRecorder& recorder,
    beast::unit_test::Suite& suite,
    Profile const& profile)
{
    {
        std::uint32_t sequence = 0;
        auto setup = [&](Env& env) {
            fundAndClose(env, {Account{"alice"}, Account{"bob"}});
            sequence = env.seq(Account{"alice"});
        };
        recordTransientScenario(
            recorder,
            suite,
            profile,
            "EscrowToken",
            "MPT_Finish_Preclaim",
            setup,
            [&](Env& env) { return insertMissingMPTEscrow(env, sequence); },
            [&](Env& env) { return makeMPTFinishPreclaim(env, sequence); },
            Expected{TER{tecOBJECT_NOT_FOUND}, true, false});
    }
    {
        std::uint32_t sequence = 0;
        auto setup = [&](Env& env) {
            fundAndClose(env, {Account{"alice"}, Account{"bob"}});
            sequence = env.seq(Account{"alice"});
        };
        recordTransientScenario(
            recorder,
            suite,
            profile,
            "EscrowToken",
            "MPT_Cancel_Preclaim",
            setup,
            [&](Env& env) { return insertMissingMPTEscrow(env, sequence); },
            [&](Env& env) { return makeMPTCancelPreclaim(env, sequence); },
            Expected{TER{tecOBJECT_NOT_FOUND}, true, false});
    }
}

void
recordQueueScenario(FixtureRecorder& recorder,
                    beast::unit_test::Suite& suite,
                    Profile const& profile)
{
    auto config = smallQueueRecorderConfig();
    configureServiceConfig(*config, profile);
    Env env{suite, std::move(config), FeatureBitset{}};
    env.app().checkSigs(true);
    env.fund(jtx::XRP(10000), jtx::noripple(Account{"alice"}));
    env.fund(jtx::XRP(200), jtx::noripple(Account{"bob"}));
    env.close();
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);
    recorder.recordWithHistory(
        profile,
        "AccountSet",
        "queued-low-fee",
        env,
        makeQueuePrefill,
        makeQueuedLowFee,
        Expected{TER{terQUEUED}, false, true});
}

void
recordQueueEvictionScenario(FixtureRecorder& recorder,
                            beast::unit_test::Suite& suite,
                            Profile const& profile)
{
    auto config = smallQueueRecorderConfig();
    configureServiceConfig(*config, profile);
    Env env{suite, std::move(config), FeatureBitset{}};
    env.app().checkSigs(true);
    setupQueueEviction(env);
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);
    json::Value history{json::ValueType::Array};
    history.append(recorder.captureHistoryStep(env, {}));
    recorder.recordWithQueueCandidates(
        profile,
        "AccountSet",
        "queue-multi-candidate-eviction-order",
        env,
        makeQueueEvictionSubmissions,
        makeQueueEvictionPrimary,
        {
            Expected{TER{tesSUCCESS}, true, false},
            Expected{TER{tesSUCCESS}, true, false},
            Expected{TER{tesSUCCESS}, true, false},
            Expected{TER{terQUEUED}, false, true},
            Expected{TER{terQUEUED}, false, true},
            Expected{TER{terQUEUED}, false, true},
            Expected{TER{terQUEUED}, false, true},
            Expected{TER{terQUEUED}, false, true},
        },
        Expected{TER{tesSUCCESS}, true, false},
        std::move(history));
}

void
recordQueueHistoryScenario(FixtureRecorder& recorder,
                           beast::unit_test::Suite& suite,
                           Profile const& profile)
{
    auto config = smallQueueRecorderConfig();
    configureServiceConfig(*config, profile);
    Env env{suite, std::move(config), FeatureBitset{}};
    env.app().checkSigs(true);
    setupQueueEviction(env);
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);

    json::Value history{json::ValueType::Array};
    history.append(recorder.captureHistoryStep(env, {}));
    history.append(recorder.captureHistoryStep(env, makeQueueHistoryFirstStep(env)));
    history.append(recorder.captureHistoryStep(env, makeQueueHistorySecondStep(env)));
    recorder.recordWithLedgerHistory(
        profile,
        "AccountSet",
        "queue-multi-ledger-history",
        env,
        std::move(history),
        makeQueueHistoryPrimary,
        Expected{TER{tesSUCCESS}, true, false});
}

template <class Builder>
void
recordServiceScenario(
    FixtureRecorder& recorder,
    beast::unit_test::Suite& suite,
    Profile const& profile,
    std::string const& family,
    std::string const& testcase,
    Builder&& builder)
{
    Env env{suite, serviceConfig(profile), FeatureBitset{}};
    env.app().checkSigs(true);
    fundAndClose(env, {Account{"alice"}, Account{"bob"}});
    assertPersistedAmendments(env, profile, suite);
    assertFreshRuntime(env, suite);
    recorder.record(profile, family, testcase, env, builder(env));
}

}  // namespace

class StrictOracleRecorder_test : public beast::unit_test::Suite
{
public:
    void
    run() override
    {
        auto const* output = std::getenv("GOXRPL_V4_FIXTURE_DIR");
        if (!output)
            return;

        FixtureRecorder recorder{*this, output};
        for (int cleanup = 0; cleanup <= 1; ++cleanup)
            for (int lending = 0; lending <= 1; ++lending)
                for (int batch = 0; batch <= 1; ++batch)
                    for (int batchFix = 0; batchFix <= 1; ++batchFix)
                    {
                        Profile const profile{
                            cleanup != 0, lending != 0, batch != 0, batchFix != 0};
                        if (!profile.batch)
                        {
                            recordWithSetup(
                                recorder,
                                *this,
                                profile,
                                "Payment",
                                "valid",
                                {Account{"alice"}, Account{"bob"}},
                                makePayment);
                            continue;
                        }

                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "Batch",
                            "canonical",
                            {Account{"alice"}, Account{"bob"}, Account{"carol"}},
                            makeBatch);
                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "Batch",
                            "poisoned-created-node-wrapper",
                            {Account{"alice"}, Account{"bob"}},
                            makePoisonedBatch);
                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "Batch",
                            "only-one-mixed",
                            {Account{"alice"}, Account{"bob"}},
                            makeBatchOnlyOneMixed,
                            Expected{TER{tesSUCCESS}, true, false});
                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "Batch",
                            "until-failure",
                            {Account{"alice"}, Account{"bob"}},
                            makeBatchUntilFailure,
                            Expected{TER{tesSUCCESS}, true, false});
                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "Batch",
                            "all-or-nothing-rollback",
                            {Account{"alice"}, Account{"bob"}},
                            makeBatchRollback,
                            Expected{TER{tesSUCCESS}, true, false});
                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "Batch",
                            "independent-mixed",
                            {Account{"alice"}, Account{"bob"}},
                            makeBatchIndependentMixed,
                            Expected{TER{tesSUCCESS}, true, false});
                    }

        for (int cleanup = 0; cleanup <= 1; ++cleanup)
            for (int lending = 0; lending <= 1; ++lending)
            {
                Profile const representative{cleanup != 0, lending != 0, true, true};
                recordWithSetup(
                    recorder,
                    *this,
                    representative,
                    "Payment",
                    "valid",
                    {Account{"alice"}, Account{"bob"}},
                    makePayment);
                recordWithSetup(
                    recorder,
                    *this,
                    representative,
                    "AccountSet",
                    "require-destination",
                    {Account{"alice"}},
                    makeAccountSet);
                recordWithSetup(
                    recorder,
                    *this,
                    representative,
                    "TrustSet",
                    "gateway-usd-limit",
                    {Account{"gateway"}, Account{"alice"}},
                    makeTrustSet);
                recordWithSetup(
                    recorder,
                    *this,
                    representative,
                    "TicketCreate",
                    "one-ticket",
                    {Account{"alice"}},
                    makeTicketCreate);
                recordWithSetup(
                    recorder,
                    *this,
                    representative,
                    "VaultCreate",
                    "xrp-vault-create",
                    {Account{"alice"}},
                    makeVaultCreate);

                if (cleanup != 0 && lending != 0)
                    recordWithSetup(
                        recorder,
                        *this,
                        representative,
                        "LoanBrokerSet",
                        "closed-ended-vault",
                        {Account{"alice"}},
                        makeLoanBrokerSet);
            }

        Profile const representative{true, true, true, true};
        recordNFTokenAuthTransientScenarios(recorder, *this, representative);
        recordEscrowTokenTransientScenarios(recorder, *this, representative);
        recordWithSetup(
            recorder,
            *this,
            representative,
            "AccountSet",
            "invalid-flags",
            {Account{"alice"}},
            makeInvalidAccountSetFlags,
            Expected{TER{temINVALID_FLAG}, false, false});
        recordWithSetup(
            recorder,
            *this,
            representative,
            "AccountSet",
            "negative-fee",
            {Account{"alice"}},
            makeNegativeFee,
            Expected{TER{temBAD_FEE}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "low-fee",
            recorderConfig(),
            setupRegularKey,
            makeLowFee,
            Expected{TER{telINSUF_FEE_P}, false, false});
        recordWithSetup(
            recorder,
            *this,
            representative,
            "AccountSet",
            "first-error-fee-before-flags",
            {Account{"alice"}},
            makeFirstError,
            Expected{TER{temBAD_FEE}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "EscrowCancel",
            "malformed-escrow-cancel-refund",
            recorderConfig(),
            setupMalformedEscrow,
            makeExpiredEscrowCancel,
            Expected{TER{tecINVARIANT_FAILED}, true, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "persistent-fee-invariant-recovery",
            recorderConfig(),
            setupFatalInvariantAccount,
            makeFatalInvariantRecovery,
            Expected{TER{tefINVARIANT_FAILED}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "queue-default-network-thresholds",
            recorderConfig(),
            [](Env& env) { fundAndClose(env, {Account{"alice"}, Account{"bob"}}); },
            makeAccountSet,
            Expected{TER{tesSUCCESS}, true, false});

        recordScenario(
            recorder,
            *this,
            representative,
            "TicketCreate",
            "insufficient-reserve",
            recorderConfig(),
            setupTicketReserveFailure,
            [](Env& env) { return env.jt(jtx::ticket::create(Account{"alice"}, 1)); },
            Expected{TER{tecINSUFFICIENT_RESERVE}, true, false});
        recordWithSetup(
            recorder,
            *this,
            representative,
            "AccountSet",
            "missing-ticket",
            {Account{"alice"}},
            makeMissingTicket,
            Expected{TER{terPRE_TICKET}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "valid-ticket",
            recorderConfig(),
            setupValidTicket,
            makeValidTicket,
            Expected{TER{tesSUCCESS}, true, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "consumed-ticket",
            recorderConfig(),
            setupConsumedTicket,
            makeConsumedTicket,
            Expected{TER{tefNO_TICKET}, false, false});

        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "past-sequence",
            recorderConfig(),
            setupPastSequence,
            makePastSequence,
            Expected{TER{tefPAST_SEQ}, false, false});
        recordWithSetup(
            recorder,
            *this,
            representative,
            "AccountSet",
            "future-sequence",
            {Account{"alice"}},
            makeFutureSequence,
            Expected{TER{terPRE_SEQ}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "expired-last-ledger",
            recorderConfig(),
            setupExpiredLedger,
            makeExpiredLedger,
            Expected{TER{tefMAX_LEDGER}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "wrong-prior-transaction",
            recorderConfig(),
            setupPriorConstraint,
            makeWrongPrior,
            Expected{TER{tefWRONG_PRIOR}, false, false});

        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "regular-key",
            recorderConfig(),
            setupRegularKey,
            makeRegularKeyPayment,
            Expected{TER{tesSUCCESS}, true, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "master-disabled",
            recorderConfig(),
            setupMasterDisabled,
            makeMasterDisabled,
            Expected{TER{tefMASTER_DISABLED}, false, false});
        recordWithSetup(
            recorder,
            *this,
            representative,
            "AccountSet",
            "wrong-key",
            {Account{"alice"}, Account{"bob"}},
            makeWrongKey,
            Expected{TER{tefBAD_AUTH}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "multisign-missing-quorum",
            recorderConfig(),
            setupMultisign,
            makeMissingMultisignQuorum,
            Expected{TER{tefBAD_QUORUM}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "multisign-valid-quorum",
            recorderConfig(),
            setupMultisign,
            makeValidMultisign,
            Expected{TER{tesSUCCESS}, true, false});

        recordScenario(
            recorder,
            *this,
            representative,
            "Payment",
            "delegate-permission-denied",
            recorderConfig(),
            setupDelegate,
            makeDelegatePermissionDenied,
            Expected{TER{terNO_DELEGATE_PERMISSION}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "Payment",
            "delegate-sponsor-fee-payer",
            recorderConfig(),
            setupDelegateSponsor,
            makeDelegateSponsorPayment,
            Expected{TER{tesSUCCESS}, true, false});

        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "network-id-missing",
            networkConfig(1025),
            [](Env& env) { fundAndClose(env, {Account{"alice"}}); },
            makeNetworkMissing,
            Expected{TER{telREQUIRES_NETWORK_ID}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "network-id-wrong",
            networkConfig(1025),
            [](Env& env) { fundAndClose(env, {Account{"alice"}}); },
            makeNetworkWrong,
            Expected{TER{telWRONG_NETWORK}, false, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "network-id-noncanonical",
            recorderConfig(),
            [](Env& env) { fundAndClose(env, {Account{"alice"}}); },
            makeNonCanonicalNetworkID,
            Expected{TER{telNETWORK_ID_MAKES_TX_NON_CANONICAL}, false, false});
        recordServiceScenario(
            recorder,
            *this,
            representative,
            "AccountSet",
            "service-boundary-amendments",
            makeAccountSet);
        recordScenario(
            recorder,
            *this,
            representative,
            "NFTokenAcceptOffer",
            "expired-sell-offer-cleanup",
            recorderConfig(),
            setupExpiredNFTokenOffer,
            makeExpiredNFTokenAccept,
            Expected{TER{tecEXPIRED}, true, false});
        recordScenario(
            recorder,
            *this,
            representative,
            "OfferCreate",
            "expired-offer-cleanup",
            recorderConfig(),
            setupExpiredOffer,
            makeExpiredOfferFillOrKill,
            Expected{TER{tecKILLED}, true, false});
        recordQueueScenario(recorder, *this, representative);
        recordQueueEvictionScenario(recorder, *this, representative);
        recordQueueHistoryScenario(recorder, *this, representative);
        for (std::uint32_t sample = 0; sample < 4; ++sample)
        {
            auto const seeded = seededPaymentSample(sample);
            recordScenario(
                recorder,
                *this,
                representative,
                "Payment",
                "seed2016-payment-" + std::to_string(sample) + "-" + seeded.label,
                recorderConfig(),
                [](Env& env) { fundAndClose(env, {Account{"alice"}, Account{"bob"}}); },
                [seeded](Env& env) { return makeSeededPayment(env, seeded); },
                seeded.expected);
        }
    }
};

BEAST_DEFINE_TESTSUITE(StrictOracleRecorder, app, xrpl);

}  // namespace xrpl::test
