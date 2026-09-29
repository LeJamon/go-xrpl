#include <test/jtx/Account.h>
#include <test/jtx/account_txn_id.h>
#include <test/jtx/delegate.h>
#include <test/jtx/Env.h>
#include <test/jtx/TestHelpers.h>
#include <test/jtx/amount.h>
#include <test/jtx/batch.h>
#include <test/jtx/envconfig.h>
#include <test/jtx/fee.h>
#include <test/jtx/flags.h>
#include <test/jtx/last_ledger_sequence.h>
#include <test/jtx/multisign.h>
#include <test/jtx/noop.h>
#include <test/jtx/pay.h>
#include <test/jtx/regkey.h>
#include <test/jtx/seq.h>
#include <test/jtx/sig.h>
#include <test/jtx/sponsor.h>
#include <test/jtx/ticket.h>
#include <test/jtx/token.h>
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
#include <xrpl/ledger/LedgerTiming.h>
#include <xrpl/ledger/OpenView.h>
#include <xrpl/ledger/ReadView.h>
#include <xrpl/protocol/Feature.h>
#include <xrpl/protocol/Indexes.h>
#include <xrpl/protocol/LedgerHeader.h>
#include <xrpl/protocol/Serializer.h>
#include <xrpl/protocol/SField.h>
#include <xrpl/protocol/SeqProxy.h>
#include <xrpl/protocol/STLedgerEntry.h>
#include <xrpl/protocol/STTx.h>
#include <xrpl/protocol/TER.h>
#include <xrpl/protocol/TxFormats.h>
#include <xrpl/protocol/TxFlags.h>
#include <xrpl/protocol/jss.h>
#include <xrpl/tx/apply.h>
#include <xrpld/app/misc/TxQ.h>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <initializer_list>
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
    submitBoundary(
        json::Value const& response,
        JTx const& transaction,
        ReadView const& postSubmitView)
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
        return result;
    }

public:
    FixtureRecorder(beast::unit_test::Suite& suite, std::filesystem::path directory)
        : suite_(suite), directory_(std::move(directory))
    {
        std::filesystem::create_directories(directory_);
    }

private:
    template <class PreBuilder, class Builder>
    void
    recordImpl(Profile const& profile,
               std::string const& family,
               std::string const& testcase,
               Env& env,
               PreBuilder&& preBuilder,
               Builder&& builder,
               Expected expected,
               json::Value txqConfigValue,
               bool includePreSubmit)
    {
        auto const parent = env.closed();
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

        if (includePreSubmit)
        {
            fixture["pre_submit"] = json::Value{json::ValueType::Array};
            for (auto const& preTransaction : preBuilder(env))
            {
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
                    submitBoundary(response, preTransaction, *postSubmitView);
                fixture["pre_submit"].append(entry);

                auto const parsed = Env::parseResult(response);
                suite_.expect(
                    parsed.ter && *parsed.ter == tesSUCCESS,
                    testcase + " pre-submit returned an unexpected TER",
                    __FILE__,
                    __LINE__);
                suite_.expect(
                    response["result"]["applied"].asBool(),
                    testcase + " pre-submit was not applied",
                    __FILE__,
                    __LINE__);
                suite_.expect(
                    !response["result"]["queued"].asBool(),
                    testcase + " pre-submit unexpectedly entered the transaction queue",
                    __FILE__,
                    __LINE__);
            }
        }

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
        fixture["submit"] = submitBoundary(response, transaction, *postSubmitView);
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
        recordImpl(
            profile,
            family,
            testcase,
            env,
            noPreSubmit,
            existingTransaction,
            expectedResult,
            txqConfig(),
            false);
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
            true);
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
        recordQueueScenario(recorder, *this, representative);
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
