#include <test/jtx/Account.h>
#include <test/jtx/Env.h>
#include <test/jtx/amount.h>
#include <test/jtx/batch.h>
#include <test/jtx/flags.h>
#include <test/jtx/pay.h>
#include <test/jtx/ticket.h>
#include <test/jtx/trust.h>
#include <test/jtx/vault.h>

#include <xrpl/basics/strHex.h>
#include <xrpl/beast/unit_test/suite.h>
#include <xrpl/json/to_string.h>
#include <xrpl/ledger/ReadView.h>
#include <xrpl/protocol/Feature.h>
#include <xrpl/protocol/LedgerHeader.h>
#include <xrpl/protocol/Serializer.h>
#include <xrpl/protocol/STLedgerEntry.h>
#include <xrpl/protocol/STTx.h>
#include <xrpl/protocol/TER.h>
#include <xrpl/protocol/TxFlags.h>
#include <xrpl/protocol/jss.h>

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

json::Value
timeValue(NetClock::time_point time)
{
    json::Value result;
    result["network_seconds"] = networkSeconds(time);
    result["iso8601"] = toStringIso(time);
    return result;
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
headerFields(LedgerHeader const& header)
{
    json::Value fields;
    fields["sequence"] = header.seq;
    fields["parent_close_time"] = timeValue(header.parentCloseTime);
    fields["close_time"] = timeValue(header.closeTime);
    fields["close_time_resolution_seconds"] = header.closeTimeResolution.count();
    fields["hash"] = to_string(header.hash);
    fields["parent_hash"] = to_string(header.parentHash);
    fields["account_hash"] = to_string(header.accountHash);
    fields["transaction_hash"] = to_string(header.txHash);
    fields["drops"] = to_string(header.drops);
    fields["validated"] = header.validated;
    fields["accepted"] = header.accepted;
    fields["close_flags"] = header.closeFlags;
    return fields;
}

json::Value
snapshot(ReadView const& view)
{
    json::Value result;
    Serializer header;
    addRaw(view.header(), header, true);
    result["header"] = strHex(header.slice());
    result["header_fields"] = headerFields(view.header());
    result["ledger_hash"] = to_string(view.header().hash);
    result["account_hash"] = to_string(view.header().accountHash);
    result["transaction_hash"] = to_string(view.header().txHash);

    result["rules"] = json::Value{json::ValueType::Array};
    result["rule_names"] = json::Value{};
    foreachFeature(test::jtx::testableAmendments(), [&](uint256 const& id) {
        if (!view.rules().enabled(id))
            return;
        auto const idString = to_string(id);
        result["rules"].append(idString);
        result["rule_names"][idString] = featureToName(id);
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

json::Value
profileJson(Profile const& profile)
{
    json::Value result;
    result["cleanup"] = profile.cleanup;
    result["lending"] = profile.lending;
    result["batch"] = profile.batch;
    result["fixBatchV1_2"] = profile.batchFix;
    result["name"] = profile.name();
    return result;
}

class FixtureRecorder
{
    beast::unit_test::Suite& suite_;
    std::filesystem::path directory_;

    static json::Value
    submitBoundary(json::Value const& response, JTx const& transaction)
    {
        json::Value result;
        if (response.isMember("result"))
        {
            auto const& rpcResult = response["result"];
            result["rpc_result"] = rpcResult;
            for (auto const field : {
                     "accepted",
                     "account_sequence_available",
                     "account_sequence_next",
                     "applied",
                     "broadcast",
                     "engine_result",
                     "engine_result_code",
                     "engine_result_message",
                     "kept",
                     "open_ledger_cost",
                     "queued",
                     "status",
                     "validated_ledger_index"})
            {
                if (rpcResult.isMember(field))
                    result[field] = rpcResult[field];
            }
            if (rpcResult.isMember("tx_json") && rpcResult["tx_json"].isMember("Fee"))
                result["fee"] = rpcResult["tx_json"]["Fee"];
            else if (transaction.jv.isMember(jss::Fee))
                result["fee"] = transaction.jv[jss::Fee];
        }
        result["rpc_response"] = response;
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
        fixture["profile_features"] = profileJson(profile);
        fixture["parent"] = snapshot(*parent);

        if (!suite_.expect(
                transaction.stx != nullptr,
                testcase + " did not produce a signed STTx",
                __FILE__,
                __LINE__))
            return;

        auto const txBlob = serialize(*transaction.stx);
        fixture["input"]["tx_blob"] = txBlob;
        fixture["input"]["tx_json"] = transaction.jv;
        fixture["input"]["tx_hash"] = to_string(transaction.stx->getTransactionID());

        auto const response = env.rpc("submit", txBlob);
        fixture["submit"] = submitBoundary(response, transaction);
        auto const parsed = Env::parseResult(response);
        if (parsed.ter)
        {
            fixture["submit"]["ter"] = transToken(*parsed.ter);
            fixture["submit"]["ter_human"] = transHuman(*parsed.ter);
            fixture["submit"]["ter_numeric"] = TERtoInt(*parsed.ter);
        }
        else
        {
            fixture["submit"]["ter"] = "telENV_RPC_FAILED";
            fixture["submit"]["ter_numeric"] = TERtoInt(telENV_RPC_FAILED);
        }
        fixture["post_submit_sle"] = sleEntries(*env.current());

        auto const openBeforeClose = env.current();
        auto const requestedClose = env.now() + std::chrono::seconds{5};
        fixture["close_input"]["requested_close_time"] = timeValue(requestedClose);
        fixture["close_input"]["parent_close_time"] =
            timeValue(openBeforeClose->header().parentCloseTime);
        fixture["close_input"]["ledger_sequence"] = openBeforeClose->header().seq;
        fixture["close_input"]["close_flags_before"] = openBeforeClose->header().closeFlags;
        fixture["close_input"]["consensus_delay_ms"] = json::Value{};
        fixture["close_input"]["transaction_blobs"] = transactionBlobs(*openBeforeClose);
        auto const closed = env.close(requestedClose);
        fixture["close_input"]["succeeded"] = closed;
        suite_.expect(closed, testcase + " ledger close failed", __FILE__, __LINE__);
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
    Env env{suite, profile.features()};
    env.app().checkSigs(true);
    fundAndClose(env, accounts);
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
                                "payment",
                                "valid",
                                {Account{"alice"}, Account{"bob"}},
                                makePayment);
                            continue;
                        }

                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "batch",
                            "canonical",
                            {Account{"alice"}, Account{"bob"}, Account{"carol"}},
                            makeBatch);
                        recordWithSetup(
                            recorder,
                            *this,
                            profile,
                            "batch",
                            "poisoned-created-node-wrapper",
                            {Account{"alice"}, Account{"bob"}},
                            makePoisonedBatch);
                    }

        Profile const representative{true, true, true, true};
        recordWithSetup(
            recorder,
            *this,
            representative,
            "payment",
            "valid",
            {Account{"alice"}, Account{"bob"}},
            makePayment);
        recordWithSetup(
            recorder,
            *this,
            representative,
            "accountset",
            "require-destination",
            {Account{"alice"}},
            makeAccountSet);
        recordWithSetup(
            recorder,
            *this,
            representative,
            "trustset",
            "gateway-usd-limit",
            {Account{"gateway"}, Account{"alice"}},
            makeTrustSet);
        recordWithSetup(
            recorder,
            *this,
            representative,
            "ticketcreate",
            "one-ticket",
            {Account{"alice"}},
            makeTicketCreate);
        recordWithSetup(
            recorder,
            *this,
            representative,
            "vault",
            "xrp-vault-create",
            {Account{"alice"}},
            makeVaultCreate);
    }
};

BEAST_DEFINE_TESTSUITE(StrictOracleRecorder, app, xrpl);

}  // namespace xrpl::test
