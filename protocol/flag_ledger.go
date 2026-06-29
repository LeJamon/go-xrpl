package protocol

// FlagLedgerInterval is the period (in ledgers) on which validators vote on
// amendments and fee/reserve changes. Matches rippled FLAG_LEDGER_INTERVAL.
const FlagLedgerInterval uint32 = 256

// IsFlagLedger reports whether the ledger at seq is a flag ledger — where fee
// and amendment vote results take effect: seq % FlagLedgerInterval == 0.
// Matches rippled isFlagLedger.
func IsFlagLedger(seq uint32) bool {
	return seq%FlagLedgerInterval == 0
}

// IsVotingLedger reports whether the validation for the ledger at seq carries
// fee- and amendment-vote fields — the ledger before a flag ledger:
// (seq+1) % FlagLedgerInterval == 0. Matches rippled isVotingLedger.
func IsVotingLedger(seq uint32) bool {
	return (seq+1)%FlagLedgerInterval == 0
}
