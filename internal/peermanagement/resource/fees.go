package resource

func FeeMalformedRequest() Charge   { return NewCharge(200, "malformed request") }
func FeeRequestNoReply() Charge     { return NewCharge(10, "unsatisfiable request") }
func FeeInvalidSignature() Charge   { return NewCharge(2000, "invalid signature") }
func FeeUselessData() Charge        { return NewCharge(150, "useless data") }
func FeeInvalidData() Charge        { return NewCharge(400, "invalid data") }
func FeeMalformedRPC() Charge       { return NewCharge(100, "malformed RPC") }
func FeeReferenceRPC() Charge       { return NewCharge(20, "reference RPC") }
func FeeExceptionRPC() Charge       { return NewCharge(100, "exceptioned RPC") }
func FeeMediumBurdenRPC() Charge    { return NewCharge(400, "medium RPC") }
func FeeHeavyBurdenRPC() Charge     { return NewCharge(3000, "heavy RPC") }
func FeeTrivialPeer() Charge        { return NewCharge(1, "trivial peer request") }
func FeeModerateBurdenPeer() Charge { return NewCharge(250, "moderate peer request") }
func FeeHeavyBurdenPeer() Charge    { return NewCharge(2000, "heavy peer request") }
func FeeWarning() Charge            { return NewCharge(4000, "received warning") }
func FeeDrop() Charge               { return NewCharge(6000, "dropped") }
