#include <test/jtx/Account.h>
#include <test/jtx/Env.h>
#include <test/jtx/TestHelpers.h>
#include <test/jtx/amount.h>
#include <test/jtx/batch.h>
#include <test/jtx/envconfig.h>
#include <test/jtx/flags.h>
#include <test/jtx/pay.h>
#include <test/jtx/ticket.h>
#include <test/jtx/trust.h>
#include <test/jtx/vault.h>

#include <xrpl/basics/strHex.h>
#include <xrpl/beast/unit_test/suite.h>
#include <xrpl/config/Constants.h>
#include <xrpl/core/NetworkIDService.h>
#include <xrpl/json/to_string.h>
#include <xrpl/ledger/LedgerTiming.h>
#include <xrpl/ledger/ReadView.h>
#include <xrpl/protocol/Feature.h>
#include <xrpl/protocol/LedgerHeader.h>
#include <xrpl/protocol/Serializer.h>
#include <xrpl/protocol/STLedgerEntry.h>
#include <xrpl/protocol/STTx.h>
#include <xrpl/protocol/TER.h>
#include <xrpl/protocol/TxFlags.h>
#include <xrpl/protocol/jss.h>
#include <xrpld/app/misc/TxQ.h>

#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <initializer_list>
#include <memory>
#include <optional>
#include <string>
#include <tuple>

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

    void
    record(Profile const& profile,
           std::string const& family,
           std::string const& testcase,
           Env& env,
           JTx const& transaction)
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
        fixture["txq_config"] = txqConfig();
        fixture["parent"] = snapshot(*parent);

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

        TER const expectedTer =
            family == "Batch" && testcase == "poisoned-created-node-wrapper" && profile.batchFix
            ? TER{temMALFORMED}
            : TER{tesSUCCESS};
        auto const expectedApplied = expectedTer == tesSUCCESS;
        suite_.expect(
            parsed.ter && *parsed.ter == expectedTer,
            testcase + " returned an unexpected TER",
            __FILE__,
            __LINE__);
        suite_.expect(
            response["result"]["applied"].asBool() == expectedApplied,
            testcase + " returned an unexpected applied flag",
            __FILE__,
            __LINE__);
        suite_.expect(
            response["result"]["queued"].asBool() == false,
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
        fixture["close_input"]["parent_close_time"] = networkSeconds(openHeader.parentCloseTime);
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
};

JTx
makePayment(Env& env)
{
    return env.jt(jtx::pay(Account{"alice"}, Account{"bob"}, jtx::XRP(1)));
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

template <class Builder>
void
recordWithSetup(FixtureRecorder& recorder,
                beast::unit_test::Suite& suite,
                Profile const& profile,
                std::string const& family,
                std::string const& testcase,
                std::initializer_list<Account> accounts,
                Builder&& builder)
{
    Env env{suite, recorderConfig(), profile.features()};
    env.app().checkSigs(true);
    fundAndClose(env, accounts);
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
    }
};

BEAST_DEFINE_TESTSUITE(StrictOracleRecorder, app, xrpl);

}  // namespace xrpl::test
