# MPT market fixtures

`Books()` and `MPTFees()` return fresh vectors shared by transaction flow and market RPC tests. These are source-derived expectations from the clean rippled `3.4.0-rc1` oracle at `2ad4def35fd8580da027462517ba3375cc005c94`, not captured RPC responses.

`Books()` covers XRP/MPT, IOU/MPT and MPT/MPT in both directions, with and without a permissioned domain. The fixed base hashes follow `Indexes.cpp::getBookBase`: the book namespace, the MPT asset tag (1, 2 or 3), ordered asset fields, any IOU issuer, then the optional domain; the last eight bytes are cleared for quality. An all-zero MPT ID represents an ordinary issue. An all-zero currency and issuer represents XRP.

`MPTFees()` covers parity, fractional fees, dust, maximum supply and maximum transfer rate. The holder-to-holder delivery is `floor(Funds * 100000 / (100000 + Fee))`; debit is `ceil(Delivered * (100000 + Fee) / 100000)`. The transfer fee reduces outstanding supply by the difference. Issuer endpoints instead use parity. These match the integral limits and transfer-fee rounding in MPTEndpointStep and BookStep.

Transaction coverage: `keylet.TestMPTMarketBookFixtures` pins every base hash; `payment.TestMPTMarketFeeFixtures` executes the two-endpoint holder transfer and checks input/output, holder balances and outstanding supply. Market RPC coverage can reuse the asset IDs and fee vectors for funded book amounts and pathfinding source amounts without copying expected arithmetic. RPC response structure remains owned by #1904.
