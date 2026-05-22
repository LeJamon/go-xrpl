package resource

// Fee schedule. Values mirror rippled's
// rippled/src/libxrpl/resource/Fees.cpp so existing operator intuition
// transfers directly. Charge labels are reused verbatim.
var (
	FeeMalformedRequest  = NewCharge(200, "malformed request")
	FeeRequestNoReply    = NewCharge(10, "unsatisfiable request")
	FeeInvalidSignature  = NewCharge(2000, "invalid signature")
	FeeUselessData       = NewCharge(150, "useless data")
	FeeInvalidData       = NewCharge(400, "invalid data")
	FeeMalformedRPC      = NewCharge(100, "malformed RPC")
	FeeReferenceRPC      = NewCharge(20, "reference RPC")
	FeeExceptionRPC      = NewCharge(100, "exceptioned RPC")
	FeeMediumBurdenRPC   = NewCharge(400, "medium RPC")
	FeeHeavyBurdenRPC    = NewCharge(3000, "heavy RPC")
	FeeTrivialPeer       = NewCharge(1, "trivial peer request")
	FeeModerateBurdenPeer = NewCharge(250, "moderate peer request")
	FeeHeavyBurdenPeer   = NewCharge(2000, "heavy peer request")
	FeeWarning           = NewCharge(4000, "received warning")
	FeeDrop              = NewCharge(6000, "dropped")
)
