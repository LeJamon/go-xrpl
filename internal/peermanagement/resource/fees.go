package resource

func FeeMalformedRequest() Charge   { return NewCharge(200, "malformed request") }
func FeeRequestNoReply() Charge     { return NewCharge(10, "unsatisfiable request") }
func FeeInvalidSignature() Charge   { return NewCharge(2000, "invalid signature") }
func FeeUselessData() Charge        { return NewCharge(150, "useless data") }
func FeeInvalidData() Charge        { return NewCharge(400, "invalid data") }
func FeeModerateBurdenPeer() Charge { return NewCharge(250, "moderate peer request") }
func FeeHeavyBurdenPeer() Charge    { return NewCharge(2000, "heavy peer request") }
func FeeDrop() Charge               { return NewCharge(6000, "dropped") }
