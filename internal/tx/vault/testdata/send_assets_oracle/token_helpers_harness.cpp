#include <test/jtx/Account.h>
#include <test/jtx/Env.h>
#include <test/jtx/amount.h>
#include <test/jtx/mpt.h>
#include <test/jtx/pay.h>
#include <test/jtx/trust.h>

#include <xrpl/beast/unit_test/suite.h>
#include <xrpl/ledger/ApplyViewImpl.h>
#include <xrpl/ledger/Sandbox.h>
#include <xrpl/ledger/helpers/TokenHelpers.h>
#include <xrpl/protocol/Feature.h>
#include <xrpl/protocol/Indexes.h>
#include <xrpl/protocol/LedgerFormats.h>
#include <xrpl/protocol/MPTIssue.h>
#include <xrpl/basics/Number.h>
#include <xrpl/protocol/SField.h>
#include <xrpl/protocol/STAmount.h>
#include <xrpl/protocol/TER.h>

#include <algorithm>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <functional>
#include <limits>
#include <memory>
#include <optional>
#include <stdexcept>
#include <sstream>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace {

using namespace xrpl;
using namespace xrpl::test::jtx;

struct Observation
{
    std::string account;
    std::string balance;
    std::string state;
};

std::string
jsonEscape(std::string_view value)
{
    std::string out;
    out.reserve(value.size() + 2);
    for (char const c : value)
    {
        switch (c)
        {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default: out += c; break;
        }
    }
    return out;
}

std::string
terName(TER const ter)
{
    return transToken(ter);
}

std::string
accountBalance(ReadView const& view, AccountID const& id)
{
    auto const sle = view.read(keylet::account(id));
    if (!sle)
        return "missing";
    return sle->getFieldAmount(sfBalance).getFullText();
}

std::string
mptBalance(ReadView const& view, MPTID const& id, AccountID const& holder)
{
    auto const sle = view.read(keylet::mptoken(id, holder));
    if (!sle)
        return "missing";
    return std::to_string(sle->getFieldU64(sfMPTAmount));
}

std::string
mptOutstanding(ReadView const& view, MPTID const& id)
{
    auto const sle = view.read(keylet::mptokenIssuance(id));
    if (!sle)
        return "missing";
    return std::to_string(sle->getFieldU64(sfOutstandingAmount));
}

std::string
mptFieldU64(ReadView const& view, MPTID const& id, SField const& field)
{
    auto const sle = view.read(keylet::mptokenIssuance(id));
    if (!sle || !sle->isFieldPresent(field))
        return {};
    return std::to_string(sle->getFieldU64(field));
}

std::string
mptFieldU16(ReadView const& view, MPTID const& id, SField const& field)
{
    auto const sle = view.read(keylet::mptokenIssuance(id));
    if (!sle || !sle->isFieldPresent(field))
        return {};
    return std::to_string(sle->getFieldU16(field));
}

std::string
mptFieldU8(ReadView const& view, MPTID const& id, SField const& field)
{
    auto const sle = view.read(keylet::mptokenIssuance(id));
    if (!sle || !sle->isFieldPresent(field))
        return {};
    return std::to_string(sle->getFieldU8(field));
}

std::string
mptHolderState(ReadView const& view, MPTID const& id, AccountID const& issuer, AccountID const& holder)
{
    if (holder == issuer)
        return "issuer";
    return view.read(keylet::mptoken(id, holder)) ? "holder" : "missing";
}

std::string
currentNumberScale()
{
    return Number::getMantissaScale() == MantissaRange::MantissaScale::Small ? "small" : "large";
}

struct Row
{
    std::string label;
    std::string asset;
    std::string profile;
    std::string sender;
    std::string issuer;
    std::string currency;
    std::string mptIssuanceID;
    std::string maximumAmount;
    std::string assetScale;
    std::optional<std::uint16_t> transferFee;
    std::string numberScale;
    std::vector<std::pair<std::string, std::string>> receivers;
    std::string ter;
    std::string senderBefore;
    std::string senderAfter;
    std::string senderBeforeState;
    std::string senderAfterState;
    std::vector<Observation> receiverBefore;
    std::vector<Observation> receiverAfter;
    std::string outstandingBefore;
    std::string outstandingAfter;
    bool sandboxBaseUnchanged = false;
    bool fixEnabled = false;
    bool mptokensV2 = false;
    bool fixUniversalNumber = true;
    bool universalNumber = true;
    bool waiveFee = true;
};

void
writeRow(std::ofstream& out, Row const& r, bool comma)
{
    if (comma)
        out << ",\n";
    out << "{\"label\":\"" << jsonEscape(r.label) << "\","
        << "\"asset\":\"" << jsonEscape(r.asset) << "\","
        << "\"profile\":\"" << jsonEscape(r.profile) << "\","
        << "\"sender\":\"" << jsonEscape(r.sender) << "\","
        << "\"issuer\":\"" << jsonEscape(r.issuer) << "\","
        << "\"currency\":\"" << jsonEscape(r.currency) << "\","
        << "\"mpt_issuance_id\":\"" << jsonEscape(r.mptIssuanceID) << "\","
        << "\"maximum_amount\":\"" << jsonEscape(r.maximumAmount) << "\","
        << "\"asset_scale\":\"" << jsonEscape(r.assetScale) << "\","
        << "\"transfer_fee\":";
    if (r.transferFee)
        out << *r.transferFee;
    else
        out << "null";
    out << ","
        << "\"number_scale\":\"" << jsonEscape(r.numberScale) << "\","
        << "\"receivers\":[";
    for (std::size_t i = 0; i < r.receivers.size(); ++i)
    {
        if (i)
            out << ',';
        out << "{\"account\":\"" << jsonEscape(r.receivers[i].first)
            << "\",\"amount\":\"" << jsonEscape(r.receivers[i].second) << "\"}";
    }
    out << "],\"ter\":\"" << jsonEscape(r.ter) << "\","
        << "\"sender_before\":\"" << jsonEscape(r.senderBefore) << "\","
        << "\"sender_after\":\"" << jsonEscape(r.senderAfter) << "\","
        << "\"receiver_before\":[";
    for (std::size_t i = 0; i < r.receiverBefore.size(); ++i)
    {
        if (i)
            out << ',';
        out << "{\"account\":\"" << jsonEscape(r.receiverBefore[i].account)
            << "\",\"balance\":\"" << jsonEscape(r.receiverBefore[i].balance)
            << "\",\"state\":\"" << jsonEscape(r.receiverBefore[i].state) << "\"}";
    }
    out << "],\"receiver_after\":[";
    for (std::size_t i = 0; i < r.receiverAfter.size(); ++i)
    {
        if (i)
            out << ',';
        out << "{\"account\":\"" << jsonEscape(r.receiverAfter[i].account)
            << "\",\"balance\":\"" << jsonEscape(r.receiverAfter[i].balance)
            << "\",\"state\":\"" << jsonEscape(r.receiverAfter[i].state) << "\"}";
    }
    out << "],\"outstanding_before\":\"" << jsonEscape(r.outstandingBefore) << "\","
        << "\"outstanding_after\":\"" << jsonEscape(r.outstandingAfter) << "\","
        << "\"sender_before_state\":\"" << jsonEscape(r.senderBeforeState) << "\","
        << "\"sender_after_state\":\"" << jsonEscape(r.senderAfterState) << "\","
        << "\"sandbox_base_unchanged\":" << (r.sandboxBaseUnchanged ? "true" : "false") << ','
        << "\"fixCleanup3_1_3\":" << (r.fixEnabled ? "true" : "false") << ','
        << "\"MPTokensV2\":" << (r.mptokensV2 ? "true" : "false") << ','
        << "\"fixUniversalNumber\":" << (r.fixUniversalNumber ? "true" : "false") << ','
        << "\"universal_number\":" << (r.universalNumber ? "true" : "false") << ','
        << "\"waive_transfer_fee\":" << (r.waiveFee ? "true" : "false") << "}\n";
}

class TokenHelpersBoundary_test : public beast::unit_test::Suite
{
    std::ofstream out_;
    bool wroteRow_ = false;

    void
    openOutput()
    {
        char const* const path = std::getenv("ISSUE_2014_EVIDENCE");
        if (!path)
            Throw<std::runtime_error>("ISSUE_2014_EVIDENCE is not set");
        out_.open(path, std::ios::out | std::ios::trunc);
        if (!out_)
            Throw<std::runtime_error>("cannot open ISSUE_2014_EVIDENCE");
        out_ << "{\"kind\":\"issue-2014-token-helpers\",\"oracle\":\"rippled-3.4.1\","
             << "\"commit\":\"d147fccf54a500fce586522f28d6044c37fd8d29\","
             << "\"rows\":[\n";
        wroteRow_ = false;
    }

    void
    closeOutput()
    {
        out_ << "]}\n";
        out_.close();
    }

    void
    emitRow(Row const& row)
    {
        writeRow(out_, row, wroteRow_);
        wroteRow_ = true;
    }

    static std::string
    idString(AccountID const& id)
    {
        return toBase58(id);
    }

    template <class AmountReader>
    Row
    runXRP(
        Env& env,
        Account const& sender,
        std::vector<std::pair<Account, Number>> const& destinations,
        std::string label,
        AmountReader const& readAmount,
        bool waiveFee = true)
    {
        auto const base = env.current();
        ApplyViewImpl parent(&*base, TapNone);
        Sandbox sandbox(&parent);
        Row row;
        row.label = std::move(label);
        row.asset = "XRP";
        row.profile = "synthetic-or-env-ledger-sandbox";
        row.sender = idString(sender.id());
        row.issuer = {};
        row.mptIssuanceID = {};
        row.maximumAmount = {};
        row.assetScale = {};
        row.transferFee = std::nullopt;
        row.numberScale = currentNumberScale();
        row.fixEnabled = sandbox.rules().enabled(fixCleanup3_1_3);
        row.mptokensV2 = sandbox.rules().enabled(featureMPTokensV2);
        row.waiveFee = waiveFee;
        row.senderBefore = readAmount(sandbox, sender.id());
        row.senderBeforeState = "xrp_account";
        row.outstandingBefore = "n/a";
        for (auto const& [account, amount] : destinations)
        {
            row.receivers.emplace_back(idString(account.id()), to_string(amount));
            row.receiverBefore.push_back({idString(account.id()), readAmount(sandbox, account.id()), "xrp_account"});
        }
        auto const parentBefore = accountBalance(parent, sender.id());
        MultiplePaymentDestinations const receivers = [&] {
            MultiplePaymentDestinations result;
            result.reserve(destinations.size());
            for (auto const& [account, amount] : destinations)
                result.emplace_back(account.id(), amount);
            return result;
        }();
        auto const ter = accountSendMulti(
            sandbox, sender.id(), Asset{xrpIssue()}, receivers, env.app().getJournal("Issue2014"));
        row.ter = terName(ter);
        row.senderAfter = readAmount(sandbox, sender.id());
        row.senderAfterState = "xrp_account";
        for (auto const& [account, amount] : destinations)
            row.receiverAfter.push_back({idString(account.id()), readAmount(sandbox, account.id()), "xrp_account"});
        row.sandboxBaseUnchanged = accountBalance(parent, sender.id()) == parentBefore;
        emitRow(row);
        return row;
    }

    Row
    runMPT(
        Env& env,
        MPTTester const& mpt,
        Account const& sender,
        std::vector<std::pair<Account, Number>> const& destinations,
        std::string label,
        bool waiveFee,
        std::function<void(Sandbox&, MPTID const&)> mutate = {})
    {
        auto const base = env.current();
        ApplyViewImpl parent(&*base, TapNone);
        Sandbox sandbox(&parent);
        Row row;
        row.label = std::move(label);
        row.asset = "MPT";
        row.profile = "synthetic-or-env-ledger-sandbox";
        row.sender = idString(sender.id());
        auto const id = mpt.issuanceID();
        auto const issuer = mpt.issuer().id();
        row.issuer = idString(issuer);
        row.mptIssuanceID = to_string(id);
        if (mutate)
            mutate(sandbox, id);
        row.maximumAmount = mptFieldU64(sandbox, id, sfMaximumAmount);
        row.assetScale = mptFieldU8(sandbox, id, sfAssetScale);
        row.transferFee = [&]() -> std::optional<std::uint16_t> {
            auto const value = mptFieldU16(sandbox, id, sfTransferFee);
            if (value.empty())
                return std::nullopt;
            return static_cast<std::uint16_t>(std::stoul(value));
        }();
        row.numberScale = currentNumberScale();
        row.fixEnabled = sandbox.rules().enabled(fixCleanup3_1_3);
        row.mptokensV2 = sandbox.rules().enabled(featureMPTokensV2);
        row.waiveFee = waiveFee;
        row.senderBefore = mptBalance(sandbox, id, sender.id());
        row.senderBeforeState = mptHolderState(sandbox, id, issuer, sender.id());
        row.outstandingBefore = mptOutstanding(sandbox, id);
        for (auto const& [account, amount] : destinations)
        {
            row.receivers.emplace_back(idString(account.id()), to_string(amount));
            row.receiverBefore.push_back({
                idString(account.id()),
                mptBalance(sandbox, id, account.id()),
                mptHolderState(sandbox, id, issuer, account.id())});
        }
        auto const parentBefore = mptBalance(parent, id, sender.id());
        MultiplePaymentDestinations const receivers = [&] {
            MultiplePaymentDestinations result;
            result.reserve(destinations.size());
            for (auto const& [account, amount] : destinations)
                result.emplace_back(account.id(), amount);
            return result;
        }();
        auto const ter = accountSendMulti(
            sandbox,
            sender.id(),
            Asset{MPTIssue{id}},
            receivers,
            env.app().getJournal("Issue2014"),
            waiveFee ? WaiveTransferFee::Yes : WaiveTransferFee::No);
        row.ter = terName(ter);
        row.senderAfter = mptBalance(sandbox, id, sender.id());
        row.senderAfterState = mptHolderState(sandbox, id, issuer, sender.id());
        row.outstandingAfter = mptOutstanding(sandbox, id);
        for (auto const& [account, amount] : destinations)
            row.receiverAfter.push_back({
                idString(account.id()),
                mptBalance(sandbox, id, account.id()),
                mptHolderState(sandbox, id, issuer, account.id())});
        row.sandboxBaseUnchanged = mptBalance(parent, id, sender.id()) == parentBefore;
        emitRow(row);
        return row;
    }

    Row
    runIOU(
        Env& env,
        Account const& issuer,
        Account const& sender,
        std::vector<std::pair<Account, Number>> const& destinations,
        std::string label,
        WaiveTransferFee waiveFee)
    {
        auto const base = env.current();
        ApplyViewImpl parent(&*base, TapNone);
        Sandbox sandbox(&parent);
        auto const issue = issuer["USD"].issue();
        auto const journal = env.app().getJournal("Issue2014");
        Row row;
        row.label = std::move(label);
        row.asset = "IOU";
        row.profile = "synthetic-or-env-ledger-sandbox";
        row.sender = idString(sender.id());
        row.issuer = idString(issuer.id());
        row.currency = "USD";
        row.numberScale = currentNumberScale();
        row.fixEnabled = sandbox.rules().enabled(fixCleanup3_1_3);
        row.mptokensV2 = sandbox.rules().enabled(featureMPTokensV2);
        row.waiveFee = waiveFee == WaiveTransferFee::Yes;
        auto readNumber = [&](ReadView const& view, AccountID const& id) {
            return to_string(accountHolds(view, id, issue, FreezeHandling::IgnoreFreeze, journal).iou());
        };
        row.senderBefore = readNumber(sandbox, sender.id());
        row.senderBeforeState = "iou_trustline";
        row.outstandingBefore = "n/a";
        for (auto const& [account, amount] : destinations)
        {
            row.receivers.emplace_back(idString(account.id()), to_string(amount));
            row.receiverBefore.push_back({
                idString(account.id()), readNumber(sandbox, account.id()), "iou_trustline"});
        }
        auto const parentBefore = readNumber(parent, sender.id());
        MultiplePaymentDestinations receivers;
        receivers.reserve(destinations.size());
        for (auto const& [account, amount] : destinations)
            receivers.emplace_back(account.id(), amount);
        auto const ter = accountSendMulti(
            sandbox,
            sender.id(),
            Asset{issue},
            receivers,
            journal,
            waiveFee);
        row.ter = terName(ter);
        row.senderAfter = readNumber(sandbox, sender.id());
        row.senderAfterState = "iou_trustline";
        for (auto const& [account, amount] : destinations)
            row.receiverAfter.push_back({
                idString(account.id()), readNumber(sandbox, account.id()), "iou_trustline"});
        row.sandboxBaseUnchanged = readNumber(parent, sender.id()) == parentBefore;
        emitRow(row);
        return row;
    }

    void
    runMPTV2OffProfile(FeatureBitset features, bool expectedFix)
    {
        Account const issuer("issue2014-v2off-issuer");
        Account const sender("issue2014-v2off-sender");
        Account const alice("issue2014-v2off-alice");
        Account const bob("issue2014-v2off-bob");
        Account const carol("issue2014-v2off-carol");
        Env env{*this, features};
        env.fund(XRP(10'000), issuer, sender, alice, bob, carol);
        env.close();

        MPTID const id = makeMptID(1, issuer);
        std::vector<Account> const holders{sender, alice, bob, carol};
        MPTTester const mpt(env, issuer, id, holders, true);
        auto const populate = [=](Sandbox& sandbox, MPTID const& issuanceID) {
            auto issuance = std::make_shared<SLE>(keylet::mptokenIssuance(issuanceID));
            issuance->setFieldU64(sfOutstandingAmount, 600);
            issuance->setFieldU64(sfMaximumAmount, 1'000);
            issuance->setFieldU16(sfTransferFee, 25);
            sandbox.insert(issuance);
            for (auto const& [account, amount] : std::initializer_list<std::pair<Account, std::uint64_t>>{
                     {sender, 400}, {alice, 100}, {bob, 100}, {carol, 0}})
            {
                auto holder = std::make_shared<SLE>(keylet::mptoken(issuanceID, account.id()));
                holder->setFieldU64(sfMPTAmount, amount);
                sandbox.insert(holder);
            }
        };

        runMPT(
            env,
            mpt,
            sender,
            {{alice, Number{10}}, {alice, Number{5}}, {sender, Number{7}}, {issuer, Number{8}}},
            expectedFix ? "mpt_v2off_holder_alias_fix_on" : "mpt_v2off_holder_alias_fix_off",
            true,
            populate);
        runMPT(
            env,
            mpt,
            sender,
            {{sender, Number{-1}}, {alice, Number{0}}},
            expectedFix ? "mpt_v2off_negative_before_self_fix_on"
                        : "mpt_v2off_negative_before_self_fix_off",
            true,
            populate);
        runMPT(
            env,
            mpt,
            issuer,
            {{alice, Number{400}}, {bob, Number{400}}},
            expectedFix ? "mpt_v2off_issuer_aggregate_max_fix_on"
                        : "mpt_v2off_issuer_aggregate_max_fix_off",
            true,
            populate);
        runMPT(
            env,
            mpt,
            sender,
            {{carol, Number{300}}, {bob, Number{300}}},
            expectedFix ? "mpt_v2off_insufficient_funds_fix_on"
                        : "mpt_v2off_insufficient_funds_fix_off",
            true,
            populate);
    }

    void
    runProfile(FeatureBitset features, bool expectedFix)
    {
        Account const issuer("issue2014-issuer");
        Account const sender("issue2014-sender");
        Account const alice("issue2014-alice");
        Account const bob("issue2014-bob");
        Account const carol("issue2014-carol");
        std::vector<Account> xrpOverflowAccounts;
        xrpOverflowAccounts.reserve(93);
        for (std::size_t i = 0; i < 93; ++i)
            xrpOverflowAccounts.emplace_back("issue2014-xrp-overflow-" + std::to_string(i));
        Env env{*this, features};
        env.fund(XRP(10'000), issuer, sender, alice, bob, carol);
        for (auto const& account : xrpOverflowAccounts)
            env.fund(XRP(10'000), account);
        env.close();

        // Ordinary XRP control: aliases, repeats, zero values, and a successful debit.
        runXRP(
            env,
            sender,
            {{alice, Number{5}}, {alice, Number{0}}, {sender, Number{7}}, {bob, Number{3}}},
            expectedFix ? "xrp_alias_repeat_zero_success_fix_on" : "xrp_alias_repeat_zero_success_fix_off",
            [](ReadView const& view, AccountID const& id) { return accountBalance(view, id); });

        // Negative amounts are rejected before the sender/receiver alias no-op check.
        runXRP(
            env,
            sender,
            {{sender, Number{-1}}, {alice, Number{0}}},
            expectedFix ? "xrp_negative_before_self_fix_on" : "xrp_negative_before_self_fix_off",
            [](ReadView const& view, AccountID const& id) { return accountBalance(view, id); });

        // The XRP aggregate accumulator is checked before each receiver credit.
        // Each recipient starts at 10,000 XRP, so use the largest credit that
        // keeps each account at the native maximum. Ninety-two such credits fit
        // in the aggregate; the 93rd overflows. This is a helper-only vector
        // because the sender cannot fund it.
        auto const maxNativeDrops = std::int64_t{100'000'000'000'000'000};
        auto const maxNativeCredit = maxNativeDrops - std::int64_t{10'000'000'000};
        std::vector<std::pair<Account, Number>> xrpOverflow;
        xrpOverflow.reserve(xrpOverflowAccounts.size());
        for (auto const& account : xrpOverflowAccounts)
            xrpOverflow.emplace_back(account, Number{maxNativeCredit});
        runXRP(
            env,
            sender,
            xrpOverflow,
            expectedFix ? "xrp_checked_aggregate_overflow_fix_on" : "xrp_checked_aggregate_overflow_fix_off",
            [](ReadView const& view, AccountID const& id) { return accountBalance(view, id); });

        // A real insufficient-funds vector demonstrates the helper's transient
        // receiver credits. The enclosing caller must discard this Sandbox.
        runXRP(
            env,
            sender,
            {{alice, Number{9'000'000'000}}, {bob, Number{9'000'000'000}}},
            expectedFix ? "xrp_insufficient_funds_partial_sandbox_fix_on"
                        : "xrp_insufficient_funds_partial_sandbox_fix_off",
            [](ReadView const& view, AccountID const& id) { return accountBalance(view, id); });

        // IOU controls cover the basic alias path and the waived aggregate
        // rounding boundary. Lending callers do not use the IOU asset, so the
        // alias rows remain explicitly non-waived while the rounding rows are
        // included for cross-implementation arithmetic parity.
        auto const usd = issuer["USD"];
        auto const iouSetupAmount = std::int64_t{1'000'000'000'000'000};
        env.trust(usd(iouSetupAmount), sender);
        env.trust(usd(iouSetupAmount), alice);
        env.trust(usd(iouSetupAmount), bob);
        env(pay(issuer, sender, usd(iouSetupAmount)));
        env.close();
        runIOU(
            env,
            issuer,
            sender,
            {{alice, Number{5}}, {alice, Number{0}}, {sender, Number{7}}, {bob, Number{3}}},
            expectedFix ? "iou_alias_repeat_control_fix_on" : "iou_alias_repeat_control_fix_off",
            WaiveTransferFee::No);
        runIOU(
            env,
            issuer,
            sender,
            {{alice, Number{3, -2}}, {bob, Number{3, -2}}},
            expectedFix ? "iou_multi_rounding_waived_fix_on" : "iou_multi_rounding_waived_fix_off",
            WaiveTransferFee::Yes);

        MPTTester mpt(env, issuer, MPTInit{.holders = {sender, alice, bob, carol}, .fund = false});
        mpt.create({.maxAmt = 1'000, .transferFee = 25'000, .flags = kMptDexFlags});
        mpt.authorize({.account = sender});
        mpt.authorize({.account = alice});
        mpt.authorize({.account = bob});
        mpt.authorize({.account = carol});
        mpt.pay(issuer, sender, 400);
        mpt.pay(issuer, alice, 100);
        mpt.pay(issuer, bob, 100);
        env.close();

        // Source/destination aliases, repeats, and a transfer-fee control. The
        // fee-waived form is the production LoanSet/LoanPay call profile.
        runMPT(
            env,
            mpt,
            sender,
            {{alice, Number{10}}, {alice, Number{5}}, {sender, Number{7}}, {issuer, Number{8}}},
            expectedFix ? "mpt_holder_alias_repeat_fee_waived_fix_on"
                        : "mpt_holder_alias_repeat_fee_waived_fix_off",
            true);
        runMPT(
            env,
            mpt,
            sender,
            {{alice, Number{10}}, {bob, Number{5}}},
            expectedFix ? "mpt_holder_transfer_fee_applied_fix_on"
                        : "mpt_holder_transfer_fee_applied_fix_off",
            false);

        // Negative amounts are rejected before self/zero no-op handling.
        runMPT(
            env,
            mpt,
            sender,
            {{sender, Number{-1}}, {alice, Number{0}}},
            expectedFix ? "mpt_negative_before_self_fix_on" : "mpt_negative_before_self_fix_off",
            true);

        // Issuer aggregate MaximumAmount and transient OutstandingAmount. The
        // post-fix path uses an exact running total; the pre-fix path preserves
        // the stale per-iteration check for replay compatibility.
        runMPT(
            env,
            mpt,
            issuer,
            {{alice, Number{400}}, {bob, Number{400}}},
            expectedFix ? "mpt_issuer_aggregate_max_fix_on" : "mpt_issuer_aggregate_max_fix_off",
            true);
        runMPT(
            env,
            mpt,
            issuer,
            {{alice, Number{100}}, {bob, Number{100}}},
            expectedFix ? "mpt_issuer_outstanding_transient_fix_on"
                        : "mpt_issuer_outstanding_transient_fix_off",
            true);

        // MPT insufficient funds after the first receiver is credited. This
        // row is helper-only because a transaction caller discards the failed
        // Sandbox and therefore cannot expose the transient state.
        runMPT(
            env,
            mpt,
            sender,
            {{carol, Number{300}}, {bob, Number{300}}},
            expectedFix ? "mpt_insufficient_funds_partial_sandbox_fix_on"
                        : "mpt_insufficient_funds_partial_sandbox_fix_off",
            true);

        // Synthetic max-int64 MPT state exercises both checked aggregate
        // accumulators. The issuance and holder balance are deliberately made
        // temporarily inconsistent inside each Sandbox; this state is not
        // reachable through a valid transaction, so rows are labelled helper-only.
        MPTTester overflowMpt(
            env,
            issuer,
            MPTInit{.holders = {sender, alice, bob}, .fund = false});
        auto const maxMPT = std::numeric_limits<std::int64_t>::max();
        overflowMpt.create({
            .maxAmt = static_cast<std::uint64_t>(maxMPT),
            .transferFee = 25'000,
            .flags = kMptDexFlags});
        overflowMpt.authorize({.account = sender});
        overflowMpt.authorize({.account = alice});
        overflowMpt.authorize({.account = bob});
        env.close();

        auto const setSyntheticMPT = [](Sandbox& sandbox, MPTID const& id, AccountID const& senderID, std::uint64_t outstanding, std::uint64_t balance) {
            auto issuance = sandbox.peek(keylet::mptokenIssuance(id));
            if (!issuance)
                Throw<std::runtime_error>("synthetic MPT issuance is missing");
            issuance->setFieldU64(sfOutstandingAmount, outstanding);
            sandbox.update(issuance);
            auto holder = sandbox.peek(keylet::mptoken(id, senderID));
            if (!holder)
                Throw<std::runtime_error>("synthetic MPT holder is missing");
            holder->setFieldU64(sfMPTAmount, balance);
            sandbox.update(holder);
        };

        runMPT(
            env,
            overflowMpt,
            sender,
            {{alice, Number{maxMPT}}, {bob, Number{maxMPT}}},
            expectedFix ? "mpt_transit_aggregate_overflow_fix_on"
                        : "mpt_transit_aggregate_overflow_fix_off",
            true,
            [=](Sandbox& sandbox, MPTID const& id) {
                setSyntheticMPT(sandbox, id, sender.id(), 0, static_cast<std::uint64_t>(maxMPT));
            });

        runMPT(
            env,
            overflowMpt,
            sender,
            {{issuer, Number{maxMPT}}, {issuer, Number{1}}},
            expectedFix ? "mpt_direct_redeem_aggregate_overflow_fix_on"
                        : "mpt_direct_redeem_aggregate_overflow_fix_off",
            true,
            [=](Sandbox& sandbox, MPTID const& id) {
                setSyntheticMPT(
                    sandbox,
                    id,
                    sender.id(),
                    static_cast<std::uint64_t>(maxMPT),
                    static_cast<std::uint64_t>(maxMPT));
            });
    }

public:
    void
    run() override
    {
        openOutput();
        FeatureBitset const all{testableAmendments()};
        runProfile(all, true);
        runProfile(all - fixCleanup3_1_3, false);
        runMPTV2OffProfile(all - featureMPTokensV2, true);
        runMPTV2OffProfile(all - featureMPTokensV2 - fixCleanup3_1_3, false);
        closeOutput();
    }
};

}  // namespace

BEAST_DEFINE_TESTSUITE(TokenHelpersBoundary, ledger, xrpl);
