package negativeunlvote

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeKey(tag byte) [33]byte {
	var key [33]byte
	key[0] = 0x02
	for i := 1; i < len(key); i++ {
		key[i] = tag
	}
	return key
}

func makeKeyN(index int) [33]byte {
	var key [33]byte
	key[0] = 0x02
	key[1] = byte(index >> 8)
	key[2] = byte(index)
	return key
}

func nodeID(tag byte) consensus.NodeID {
	return keyToNodeID(makeKey(tag))
}

func healthyScores(myID consensus.NodeID, keys [][33]byte) map[consensus.NodeID]uint32 {
	scores := make(map[consensus.NodeID]uint32, len(keys))
	for _, key := range keys {
		scores[keyToNodeID(key)] = highWaterMark + 1
	}
	scores[myID] = minLocalValsToVote + 1
	return scores
}

type expectedVote struct {
	disabling uint8
	validator [33]byte
}

func requireVotes(t *testing.T, blobs [][]byte, seq uint32, expected ...expectedVote) {
	t.Helper()
	require.Len(t, blobs, len(expected))
	for i, want := range expected {
		decoded, err := binarycodec.Decode(hex.EncodeToString(blobs[i]))
		require.NoError(t, err)
		assert.EqualValues(t, want.disabling, decoded["UNLModifyDisabling"])
		assert.EqualValues(t, seq, decoded["LedgerSequence"])
		validator, ok := decoded["UNLModifyValidator"].(string)
		require.True(t, ok)
		assert.True(t, strings.EqualFold(hex.EncodeToString(want.validator[:]), validator))
	}
}

func TestStateEffectiveNegUNL(t *testing.T) {
	a := makeKey(0xA1)
	b := makeKey(0xB2)
	c := makeKey(0xC3)

	tests := []struct {
		name  string
		state State
		want  [][33]byte
	}{
		{name: "disabled", state: State{DisabledKeys: [][33]byte{a, b}}, want: [][33]byte{a, b}},
		{name: "pending disable", state: State{DisabledKeys: [][33]byte{a, b}, ToDisablePending: &c}, want: [][33]byte{a, b, c}},
		{name: "pending re-enable", state: State{DisabledKeys: [][33]byte{a, b}, ToReEnablePending: &b}, want: [][33]byte{a}},
		{name: "both pending", state: State{DisabledKeys: [][33]byte{a, b}, ToDisablePending: &c, ToReEnablePending: &a}, want: [][33]byte{b, c}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.effectiveNegUNL()
			require.Len(t, got, len(tt.want))
			for _, key := range tt.want {
				assert.Contains(t, got, key)
			}
		})
	}
}

func TestDoVotingParticipationGate(t *testing.T) {
	myKey := makeKey(0xAA)
	weak := makeKey(0xBB)

	tests := []struct {
		name       string
		count      uint32
		localInUNL bool
		wantErr    error
		wantVote   bool
	}{
		{name: "below threshold", count: minLocalValsToVote - 1, localInUNL: true},
		{name: "exact threshold", count: minLocalValsToVote, localInUNL: true, wantErr: ErrLocalCountAtThreshold},
		{name: "above threshold", count: minLocalValsToVote + 1, localInUNL: true, wantVote: true},
		{name: "at window", count: protocol.FlagLedgerInterval, localInUNL: true, wantVote: true},
		{name: "above window", count: protocol.FlagLedgerInterval + 1, localInUNL: true, wantErr: ErrLocalCountExceedsWindow},
		{name: "local not in UNL", count: protocol.FlagLedgerInterval, localInUNL: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			voter := NewVoter(keyToNodeID(myKey))
			unl := [][33]byte{weak}
			if tt.localInUNL {
				unl = append(unl, myKey)
			}
			scores := map[consensus.NodeID]uint32{
				voter.myID:        tt.count,
				keyToNodeID(weak): lowWaterMark - 1,
			}

			blobs, err := voter.DoVoting(1024, [32]byte{0xDE, 0xAD}, unl, State{}, scores)
			if tt.wantVote {
				require.Len(t, blobs, 1)
			} else {
				assert.Nil(t, blobs)
			}
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestDoVotingOutcomes(t *testing.T) {
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	good2 := makeKey(0xBC)
	weak := makeKey(0xCC)
	weak2 := makeKey(0xCD)
	disabled := makeKey(0xDD)
	stale := makeKey(0xEE)
	stray := makeKey(0xEF)

	tests := []struct {
		name     string
		unl      [][33]byte
		state    State
		pad      [32]byte
		mutate   func(map[consensus.NodeID]uint32)
		expected []expectedVote
	}{
		{
			name: "all participating",
			unl:  [][33]byte{myKey, good},
		},
		{
			name: "disable low score",
			unl:  [][33]byte{myKey, good, weak},
			mutate: func(scores map[consensus.NodeID]uint32) {
				scores[keyToNodeID(weak)] = lowWaterMark - 1
			},
			expected: []expectedVote{{disabling: 1, validator: weak}},
		},
		{
			name:     "re-enable recovered validator",
			unl:      [][33]byte{myKey, good, disabled},
			state:    State{DisabledKeys: [][33]byte{disabled}},
			expected: []expectedVote{{disabling: 0, validator: disabled}},
		},
		{
			name:  "listed cap prevents disable",
			unl:   [][33]byte{myKey, good, weak, disabled},
			state: State{DisabledKeys: [][33]byte{disabled}},
			mutate: func(scores map[consensus.NodeID]uint32) {
				scores[keyToNodeID(weak)] = lowWaterMark - 1
				scores[keyToNodeID(disabled)] = highWaterMark
			},
		},
		{
			name:     "retired validator fallback",
			unl:      [][33]byte{myKey, good},
			state:    State{DisabledKeys: [][33]byte{stale}},
			expected: []expectedVote{{disabling: 0, validator: stale}},
		},
		{
			name: "missing score is zero",
			unl:  [][33]byte{myKey, good, weak},
			mutate: func(scores map[consensus.NodeID]uint32) {
				delete(scores, keyToNodeID(weak))
			},
			expected: []expectedVote{{disabling: 1, validator: weak}},
		},
		{
			name: "stray score is ignored",
			unl:  [][33]byte{myKey, weak},
			pad: func() [32]byte {
				var pad [32]byte
				strayID := keyToNodeID(stray)
				copy(pad[:], strayID[:])
				return pad
			}(),
			mutate: func(scores map[consensus.NodeID]uint32) {
				scores[keyToNodeID(weak)] = lowWaterMark - 1
				scores[keyToNodeID(stray)] = 0
			},
			expected: []expectedVote{{disabling: 1, validator: weak}},
		},
		{
			name: "multiple disable candidates emit one vote",
			unl:  [][33]byte{myKey, good, weak, weak2},
			pad: func() [32]byte {
				var pad [32]byte
				weakID := keyToNodeID(weak)
				copy(pad[:], weakID[:])
				return pad
			}(),
			mutate: func(scores map[consensus.NodeID]uint32) {
				scores[keyToNodeID(weak)] = lowWaterMark - 1
				scores[keyToNodeID(weak2)] = lowWaterMark - 1
			},
			expected: []expectedVote{{disabling: 1, validator: weak}},
		},
		{
			name:  "one vote in each direction",
			unl:   [][33]byte{myKey, good, good2, weak, disabled},
			state: State{DisabledKeys: [][33]byte{disabled}},
			mutate: func(scores map[consensus.NodeID]uint32) {
				scores[keyToNodeID(weak)] = lowWaterMark - 1
			},
			expected: []expectedVote{{disabling: 1, validator: weak}, {disabling: 0, validator: disabled}},
		},
		{
			name:     "pending disable is effective",
			unl:      [][33]byte{myKey, good, disabled},
			state:    State{ToDisablePending: &disabled},
			expected: []expectedVote{{disabling: 0, validator: disabled}},
		},
		{
			name:  "pending re-enable is effective",
			unl:   [][33]byte{myKey, good, disabled},
			state: State{DisabledKeys: [][33]byte{disabled}, ToReEnablePending: &disabled},
			mutate: func(scores map[consensus.NodeID]uint32) {
				scores[keyToNodeID(disabled)] = lowWaterMark - 1
			},
			expected: []expectedVote{{disabling: 1, validator: disabled}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			voter := NewVoter(keyToNodeID(myKey))
			scores := healthyScores(voter.myID, tt.unl)
			if tt.mutate != nil {
				tt.mutate(scores)
			}
			blobs, err := voter.DoVoting(1024, tt.pad, tt.unl, tt.state, scores)
			require.NoError(t, err)
			requireVotes(t, blobs, 1025, tt.expected...)
		})
	}
}

func TestDoVotingNewValidatorGrace(t *testing.T) {
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	fresh := makeKey(0xCC)
	unl := [][33]byte{myKey, good, fresh}
	voter := NewVoter(keyToNodeID(myKey))
	scores := healthyScores(voter.myID, unl)
	scores[keyToNodeID(fresh)] = lowWaterMark - 1

	const addedAt = uint32(900)
	voter.NewValidators(addedAt, []consensus.NodeID{keyToNodeID(fresh)})
	voter.NewValidators(addedAt+newValidatorDisableSkip-10, []consensus.NodeID{keyToNodeID(fresh)})

	tests := []struct {
		name     string
		prevSeq  uint32
		expected []expectedVote
	}{
		{name: "within grace", prevSeq: addedAt},
		{name: "exact grace boundary", prevSeq: addedAt + newValidatorDisableSkip - 1},
		{name: "after original grace despite repeat", prevSeq: addedAt + newValidatorDisableSkip, expected: []expectedVote{{disabling: 1, validator: fresh}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobs, err := voter.DoVoting(tt.prevSeq, [32]byte{0x04}, unl, State{}, scores)
			require.NoError(t, err)
			requireVotes(t, blobs, tt.prevSeq+1, tt.expected...)
		})
	}

	t.Run("fresh disabled validator can re-enable", func(t *testing.T) {
		voter := NewVoter(keyToNodeID(myKey))
		voter.NewValidators(addedAt, []consensus.NodeID{keyToNodeID(fresh)})
		blobs, err := voter.DoVoting(
			addedAt,
			[32]byte{0x04},
			unl,
			State{DisabledKeys: [][33]byte{fresh}},
			healthyScores(voter.myID, unl),
		)
		require.NoError(t, err)
		requireVotes(t, blobs, addedAt+1, expectedVote{disabling: 0, validator: fresh})
	})
}

func TestChooseOracleGoldens(t *testing.T) {
	var one, two, three consensus.NodeID
	one[len(one)-1] = 1
	two[len(two)-1] = 2
	three[len(three)-1] = 3

	var allFF [32]byte
	for i := range allFF {
		allFF[i] = 0xFF
	}
	lastBytesOnly := [32]byte{}
	for i := len(one); i < len(lastBytesOnly); i++ {
		lastBytesOnly[i] = 0xFF
	}

	tests := []struct {
		name       string
		pad        [32]byte
		candidates []consensus.NodeID
		want       consensus.NodeID
	}{
		{name: "single zero pad", candidates: []consensus.NodeID{one}, want: one},
		{name: "single FF pad", pad: allFF, candidates: []consensus.NodeID{one}, want: one},
		{name: "zero pad", candidates: []consensus.NodeID{one, two, three}, want: one},
		{name: "zero pad reversed", candidates: []consensus.NodeID{three, two, one}, want: one},
		{name: "FF pad", pad: allFF, candidates: []consensus.NodeID{one, two, three}, want: three},
		{name: "FF pad reversed", pad: allFF, candidates: []consensus.NodeID{three, two, one}, want: three},
		{name: "last twelve pad bytes ignored", pad: lastBytesOnly, candidates: []consensus.NodeID{three, two, one}, want: one},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, choose(tt.pad, tt.candidates))
		})
	}
}

func buildCandidateFixture(unlSize, negPercent int, scores []uint32) (
	map[consensus.NodeID][33]byte,
	map[consensus.NodeID]struct{},
	map[consensus.NodeID]uint32,
) {
	nodeIDs := make([]consensus.NodeID, unlSize)
	unl := make(map[consensus.NodeID][33]byte, unlSize)
	scoreTable := make(map[consensus.NodeID]uint32, unlSize)
	for i := range unlSize {
		key := makeKeyN(i)
		nodeIDs[i] = keyToNodeID(key)
		unl[nodeIDs[i]] = key
		scoreTable[nodeIDs[i]] = scores[i]
	}
	negUNL := make(map[consensus.NodeID]struct{})
	if negPercent == 100 {
		for _, id := range nodeIDs {
			negUNL[id] = struct{}{}
		}
	} else if negPercent == 50 {
		for i := 1; i < len(nodeIDs); i += 2 {
			negUNL[nodeIDs[i]] = struct{}{}
		}
	}
	return unl, negUNL, scoreTable
}

func TestFindAllCandidatesOracleCombinations(t *testing.T) {
	unlSizes := []int{34, 35, 80}
	negPercents := []int{0, 50, 100}
	scoreCases := []struct {
		name     string
		score    uint32
		disable  bool
		reEnable bool
	}{
		{name: "zero", score: 0, disable: true},
		{name: "below low", score: lowWaterMark - 1, disable: true},
		{name: "at low", score: lowWaterMark},
		{name: "above low", score: lowWaterMark + 1},
		{name: "below high", score: highWaterMark - 1},
		{name: "at high", score: highWaterMark},
		{name: "above high", score: highWaterMark + 1, reEnable: true},
		{name: "local threshold", score: minLocalValsToVote, reEnable: true},
	}
	voter := NewVoter(nodeID(0xA0))

	t.Run("uniform scores", func(t *testing.T) {
		for _, size := range unlSizes {
			for _, negPercent := range negPercents {
				for _, scoreCase := range scoreCases {
					name := fmt.Sprintf("size=%d/neg=%d/%s", size, negPercent, scoreCase.name)
					t.Run(name, func(t *testing.T) {
						scores := make([]uint32, size)
						for i := range scores {
							scores[i] = scoreCase.score
						}
						unl, negUNL, scoreTable := buildCandidateFixture(size, negPercent, scores)
						got := voter.findAllCandidates(unl, negUNL, scoreTable)

						wantDisable := 0
						if negPercent == 0 && scoreCase.disable {
							wantDisable = size
						}
						wantReEnable := 0
						if negPercent != 0 && scoreCase.reEnable {
							wantReEnable = len(negUNL)
						}
						assert.Len(t, got.toDisable, wantDisable)
						assert.Len(t, got.toReEnable, wantReEnable)
					})
				}
			}
		}
	})

	t.Run("mixed scores", func(t *testing.T) {
		mixed := []uint32{
			0, 0,
			lowWaterMark - 1, lowWaterMark - 1,
			lowWaterMark, lowWaterMark,
			lowWaterMark + 1, lowWaterMark + 1,
			highWaterMark - 1, highWaterMark - 1,
			highWaterMark, highWaterMark,
			highWaterMark + 1, highWaterMark + 1,
			minLocalValsToVote, minLocalValsToVote,
		}
		for _, size := range unlSizes {
			for _, negPercent := range negPercents {
				name := fmt.Sprintf("size=%d/neg=%d", size, negPercent)
				t.Run(name, func(t *testing.T) {
					scores := make([]uint32, size)
					copy(scores, mixed)
					for i := len(mixed); i < size; i++ {
						scores[i] = minLocalValsToVote
					}
					unl, negUNL, scoreTable := buildCandidateFixture(size, negPercent, scores)
					got := voter.findAllCandidates(unl, negUNL, scoreTable)

					wantDisable := 0
					wantReEnable := 0
					switch negPercent {
					case 0:
						wantDisable = 4
					case 50:
						wantReEnable = len(negUNL) - 6
					case 100:
						wantReEnable = len(negUNL) - 12
					}
					assert.Len(t, got.toDisable, wantDisable)
					assert.Len(t, got.toReEnable, wantReEnable)
				})
			}
		}
	})
}

func TestFindAllCandidatesFallbackPriority(t *testing.T) {
	a := makeKey(0xA1)
	b := makeKey(0xB2)
	retired := makeKey(0xC3)
	unl := map[consensus.NodeID][33]byte{
		keyToNodeID(a): a,
		keyToNodeID(b): b,
	}
	negUNL := map[consensus.NodeID]struct{}{
		keyToNodeID(b):       {},
		keyToNodeID(retired): {},
	}
	voter := NewVoter(nodeID(0xA0))

	scores := map[consensus.NodeID]uint32{keyToNodeID(a): highWaterMark, keyToNodeID(b): highWaterMark}
	got := voter.findAllCandidates(unl, negUNL, scores)
	assert.Equal(t, []consensus.NodeID{keyToNodeID(retired)}, got.toReEnable)

	scores[keyToNodeID(b)] = highWaterMark + 1
	got = voter.findAllCandidates(unl, negUNL, scores)
	assert.Equal(t, []consensus.NodeID{keyToNodeID(b)}, got.toReEnable)
}

func TestBuildUNLModifyTxWireFormat(t *testing.T) {
	validator := makeKey(0x42)
	blob, err := buildUNLModifyTx(99999, validator, toDisable)
	require.NoError(t, err)

	hexBlob := hex.EncodeToString(blob)
	assert.Contains(t, hexBlob, "73007013")
	assert.NotContains(t, hexBlob, "2200000000")

	decoded, err := binarycodec.Decode(hexBlob)
	require.NoError(t, err)
	assert.Equal(t, "", decoded["SigningPubKey"])
	_, hasFlags := decoded["Flags"]
	assert.False(t, hasFlags)
	assert.EqualValues(t, 1, decoded["UNLModifyDisabling"])
	assert.EqualValues(t, uint32(99999), decoded["LedgerSequence"])
}
