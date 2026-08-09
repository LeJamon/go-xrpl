package list

import (
	"encoding/hex"
	"slices"
	"sort"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// ApplyList ingests a single (manifest, blob, signature) triple and returns
// its disposition, publisher master key, and sequence when extractable.
//
// `manifestBytes` and `blob` carry the WIRE-FORM ascii strings as
// received in TMValidatorList / TMValidatorListCollection (base64-
// encoded). `signature` carries the WIRE-FORM hex string. `version`
// is the protocol version negotiated at the message level.
func (a *Aggregator) ApplyList(manifestBytes, blob, signature []byte, version uint32, siteURI string) (Disposition, PublisherKey, uint32) {
	return a.applyListInternal(manifestBytes, nil, false, blob, signature, version, siteURI, false)
}

// applyListInternal is the common v1/v2 ingest path. globalManifest is the
// collection-level publisher manifest; localManifest is an optional per-blob
// override. localManifestSet must be true even for an explicitly empty local
// manifest so the caller does not silently fall back to the global value.
func (a *Aggregator) applyListInternal(globalManifest, localManifest []byte, localManifestSet bool, blob, signature []byte, version uint32, siteURI string, asyncDispatch bool) (Disposition, PublisherKey, uint32) {
	if !isSupportedVersion(version) {
		return UnsupportedVersion, PublisherKey{}, 0
	}

	manifestBytes := globalManifest
	if localManifestSet {
		manifestBytes = localManifest
	}

	if len(manifestBytes) > manifest.MaxManifestBase64 {
		a.logger.Debug("validator list: manifest exceeds maximum base64 size", "site", siteURI)
		return Untrusted, PublisherKey{}, 0
	}
	manifestRaw, err := decodeBase64Tolerant(manifestBytes)
	if err != nil {
		a.logger.Debug("validator list: manifest base64 decode failed", "error", err, "site", siteURI)
		return Invalid, PublisherKey{}, 0
	}
	parsed, err := manifest.Deserialize(manifestRaw)
	if err != nil {
		a.logger.Debug("validator list: manifest deserialize failed", "error", err, "site", siteURI)
		return Invalid, PublisherKey{}, 0
	}
	masterKey := parsed.MasterKey()
	pubKey := PublisherKey(masterKey)

	// Reject lists from publishers we don't trust. Per rippled this is
	// a silent drop — gossip carries lists from many publishers and we
	// shouldn't penalize peers for forwarding lists we choose not to
	// trust ourselves.
	a.mu.Lock()
	_, trusted := a.publishers[pubKey]
	a.mu.Unlock()
	if !trusted {
		return Untrusted, pubKey, 0
	}

	// Track the cache disposition so revocation only flips publisher
	// state when the manifest cache actually accepted the revocation.
	// Mirrors rippled ValidatorList.cpp:1373-1378 — `removePublisherList`
	// runs only under `revoked && result == ManifestDisposition::accepted`;
	// a stale revocation (cache already holds a higher-sequence
	// non-revoked manifest) returns untrusted without clearing state.
	manifestAccepted := false
	var manifestSequence uint32
	manifestSequenceSet := false
	if a.publisherManifests != nil {
		switch d := a.publisherManifests.ApplyManifest(parsed, manifest.Uncapped); d {
		case manifest.Accepted:
			manifestAccepted = true
		case manifest.Stale:
		case manifest.Invalid:
			// The manifest was structurally valid, but its signatures or
			// cache invariants were not. Rippled maps this path to
			// ListDisposition::untrusted (ValidatorList.cpp:1360-1363).
			return Untrusted, pubKey, 0
		case manifest.BadMasterKey, manifest.BadEphemeralKey:
			// Cache state is unchanged for these (Manifest.cpp:436-477
			// returns before any mutation), so `getSigningKey(masterPubKey)`
			// below will still return the previously-cached signing key if
			// any. Rippled's check at ValidatorList.cpp:1380-1383 gates only
			// on `result == invalid`, NOT on badMasterKey/badEphemeralKey,
			// so we fall through to the signing-key lookup. If the cache
			// has no key for this master, the lookup branch a few lines
			// below returns Untrusted; if it has one, blob verification
			// proceeds against that cached key — matching rippled.
		}
	} else {
		// Fall back to direct verification when no cache is wired
		// (tests). The signing key in the manifest is what we'd have
		// pulled from the cache. Mirrors rippled's invalid-manifest →
		// untrusted mapping at ValidatorList.cpp:1382-1383.
		if err := parsed.Verify(); err != nil {
			return Untrusted, pubKey, 0
		}
		manifestAccepted = true
	}

	if parsed.Revoked() {
		// Rippled returns ListDisposition::untrusted on revocation
		// (ValidatorList.cpp:1382-1383). Revocations are legitimate
		// gossip; punishing the forwarding peer would cascade across
		// every honest hop in the mesh. The state-clearing side effect
		// only runs when the manifest cache actually accepted the
		// revocation — mirrors rippled's `revoked && result == accepted`
		// gate at ValidatorList.cpp:1373.
		if manifestAccepted {
			a.handleRevocation(pubKey, asyncDispatch)
		}
		return Untrusted, pubKey, 0
	}

	// Pull the current ephemeral signing key. With a cache: this is
	// the freshest signing key we've ever seen for the publisher,
	// which might be NEWER than the one in this very manifest if a
	// later manifest arrived first via gossip. Rippled also uses
	// publisherManifests_.getSigningKey here, which is the latest
	// cached key, not the one in `manifestBytes`.
	signingKey := parsed.SigningKey()
	if a.publisherManifests != nil {
		if k, ok := a.publisherManifests.GetSigningKey(masterKey); ok {
			signingKey = k
			manifestSequence, manifestSequenceSet = a.publisherManifests.GetSequence(masterKey)
		} else {
			// Cache says the master is unknown or revoked. If revoked
			// we already handled above; if unknown despite the apply,
			// treat as untrusted (no usable signing key from a trusted
			// publisher means we cannot verify the blob; rippled's
			// equivalent at ValidatorList.cpp:1382 is also untrusted).
			return Untrusted, pubKey, 0
		}
	}

	if err := verifyBlobSignature(signingKey, blob, signature); err != nil {
		a.logger.Debug("validator list: blob signature invalid", "error", err, "publisher", hex.EncodeToString(pubKey[:]))
		return Invalid, pubKey, 0
	}

	parsedBlob, disp, err := parseBlob(blob)
	if err != nil {
		a.logger.Debug("validator list: blob parse failed", "error", err, "publisher", hex.EncodeToString(pubKey[:]))
		return disp, pubKey, 0
	}

	now := a.clock()
	validFrom := time.Unix(rippleSecondsToUnix(parsedBlob.Effective), 0).UTC()
	validUntil := time.Unix(rippleSecondsToUnix(parsedBlob.Expiration), 0).UTC()

	a.mu.Lock()
	if asyncDispatch {
		defer a.dispatchChangesAsync()
	} else {
		defer a.dispatchChanges()
	}
	defer a.flushCacheWrites()
	defer a.mu.Unlock()

	// New() pre-populates state[pubKey] for every trusted publisher and
	// the entry is never deleted, so a missing key would be an internal
	// invariant break — surface it loudly rather than silently re-create.
	current := a.state[pubKey]
	if current == nil {
		a.logger.Error("validator list: trusted publisher state missing", "publisher", hex.EncodeToString(pubKey[:]))
		return Untrusted, pubKey, 0
	}
	if a.beforeListCommit != nil {
		a.beforeListCommit()
	}

	commit := func() (Disposition, uint32) {
		// Match applyLists' post-apply cleanup before evaluating the next
		// disposition. Ready entries are deliberately not promoted here: a
		// re-arrival at a ready pending sequence must take rippled's Accepted
		// promotion path below, rather than becoming SameSequence.
		a.normalizeRemainingLocked(current)

		// Determine disposition by sequence + time ordering. Mirrors the
		// rippled state machine at ValidatorList.cpp:1394-1437. The
		// SameSequence branch is intentionally unguarded by status —
		// rippled returns same_sequence for every repeat of the current
		// sequence regardless of `pubCollection.status`.
		if parsedBlob.Sequence < current.Sequence {
			return Stale, 0
		}
		if parsedBlob.Sequence == current.Sequence {
			return SameSequence, current.MaxSequence
		}
		expired := validUntil.Before(now) || validUntil.Equal(now)
		if expired || !validFrom.After(now) {
			// Expired ingest runs the SAME populate path as accepted in
			// rippled: ValidatorList.cpp:1193-1295 — the local `accepted`
			// boolean is true for both ListDisposition::accepted AND
			// ListDisposition::expired, so the populate block writes
			// publisher.list and publisher.manifests, and the call to
			// updatePublisherList at line 1294 seeds embedded validator
			// manifests into validatorManifests_ (line 1117-1133). The only
			// runtime differences vs accepted are the final PublisherStatus
			// (expired vs available) and that the trusted-set recompute
			// skips non-available publishers (see recomputeAndEmitLocked:
			// `s.Status != StatusAvailable` filter).
			//
			// removePublisherList(StatusExpired) at ValidatorList.cpp:1529-1542
			// is invoked from updateTrusted (line 1999) when a previously-
			// available list times out at ledger close — NOT from applyList.
			if _, pending := current.Remaining[parsedBlob.Sequence]; pending {
				a.promotePendingSequenceLocked(current, parsedBlob.Sequence, now)
			} else {
				a.applyAcceptedLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, now, version, globalManifest, localManifest, localManifestSet, blob, signature)
				a.recordMaxSequenceLocked(current, parsedBlob.Sequence)
			}
			if expired {
				current.Status = StatusExpired
			}
			a.normalizeRemainingLocked(current)
			a.writeCacheLocked(current)
			a.recomputeAndEmitLocked()
			if expired {
				return Expired, current.MaxSequence
			}
			return Accepted, current.MaxSequence
		}
		if validFrom.After(now) {
			// Future-dated. Mirrors rippled ValidatorList.cpp:1414-1432
			// pending-vs-known_sequence: a list is "pending" the first
			// time it lands or whenever its sequence is the largest seen,
			// and "known_sequence" only for re-arrivals at an already-
			// queued sequence. The queued blob is retained so the next
			// promoteRemainingLocked pass can rotate it into `current`.
			d := a.applyPendingLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, version, globalManifest, localManifest, localManifestSet, blob, signature)
			a.normalizeRemainingLocked(current)
			if d == Pending {
				a.writeCacheLocked(current)
			}
			return d, current.MaxSequence
		}

		a.applyAcceptedLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, now, version, globalManifest, localManifest, localManifestSet, blob, signature)
		a.recordMaxSequenceLocked(current, parsedBlob.Sequence)
		a.normalizeRemainingLocked(current)
		a.writeCacheLocked(current)
		a.recomputeAndEmitLocked()
		return Accepted, current.MaxSequence
	}

	if a.publisherManifests == nil {
		disp, sequence := commit()
		return disp, pubKey, sequence
	}
	if !manifestSequenceSet {
		return Untrusted, pubKey, 0
	}
	var disposition Disposition
	var sequence uint32
	if !a.publisherManifests.WithCurrent(masterKey, signingKey, manifestSequence, func() {
		disposition, sequence = commit()
	}) {
		return Untrusted, pubKey, 0
	}
	return disposition, pubKey, sequence
}

func (s *publisherState) updateRawBytes(manifest, blob, signature []byte) {
	s.RawManifest = append(s.RawManifest[:0], manifest...)
	s.RawBlob = append(s.RawBlob[:0], blob...)
	s.RawSignature = append(s.RawSignature[:0], signature...)
}

func (s *publisherState) updateRawLocalManifest(raw []byte, present bool) {
	s.RawLocalManifest = append(s.RawLocalManifest[:0], raw...)
	s.RawLocalManifestSet = present
}

func (a *Aggregator) extractValidatorKeys(blob *blobJSON, logMsg string) [][33]byte {
	keys := make([][33]byte, 0, len(blob.Validators))
	for i, v := range blob.Validators {
		raw, err := hex.DecodeString(v.ValidationPublicKey)
		if err != nil || !validatorKeyValid(raw) {
			// Mirrors rippled ValidatorList.cpp:1250-1273 which logs
			// `Invalid node identity` and silently skips the entry
			// rather than rejecting the surrounding blob.
			a.logger.Debug(logMsg,
				"index", i,
				"pubkey", v.ValidationPublicKey)
			continue
		}
		var k [33]byte
		copy(k[:], raw)
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i][:]) < string(keys[j][:])
	})
	return keys
}

func (a *Aggregator) applyAcceptedLocked(s *publisherState, blob *blobJSON, signingKey [33]byte, validFrom, validUntil time.Time, siteURI string, now time.Time, version uint32, rawManifest, rawLocalManifest []byte, rawLocalManifestSet bool, rawBlob, rawSignature []byte) {
	s.Sequence = blob.Sequence
	s.Effective = validFrom
	s.EffectiveSet = blob.EffectiveSet
	s.Expiration = validUntil
	s.SigningKey = signingKey
	s.SiteURI = siteURI
	s.LastUpdate = now
	s.Status = StatusAvailable
	if version > s.Version {
		s.Version = version
	}
	s.updateRawBytes(rawManifest, rawBlob, rawSignature)
	s.updateRawLocalManifest(rawLocalManifest, rawLocalManifestSet)

	keys := a.extractValidatorKeys(blob, "validator list: skipping invalid validator entry")
	s.Validators = keys
	a.applyEmbeddedManifestsLocked(s, blob.Validators)
}

// applyEmbeddedManifestsLocked applies validator manifests from an effective
// blob after checking that their master keys occur in at least one live list.
// Caller must hold a.mu.
func (a *Aggregator) applyEmbeddedManifestsLocked(s *publisherState, entries []blobEntryJS) {
	if a.validatorManifests == nil {
		return
	}
	listed := make(map[[33]byte]struct{}, len(s.Validators)+8)
	for _, k := range s.Validators {
		listed[k] = struct{}{}
	}
	for pubMaster, ps := range a.state {
		if pubMaster == s.MasterKey {
			continue
		}
		for _, k := range ps.Validators {
			listed[k] = struct{}{}
		}
	}
	for _, v := range entries {
		if v.Manifest == "" {
			continue
		}
		if len(v.Manifest) > manifest.MaxManifestBase64 {
			continue
		}
		raw, err := decodeBase64Tolerant([]byte(v.Manifest))
		if err != nil {
			continue
		}
		a.applyEmbeddedManifestRawLocked(listed, raw)
	}
}

func (a *Aggregator) applyEmbeddedManifestRawLocked(listed map[[33]byte]struct{}, raw []byte) {
	parsed, err := manifest.Deserialize(raw)
	if err != nil {
		return
	}
	masterKey := parsed.MasterKey()
	if _, ok := listed[masterKey]; !ok {
		a.logger.Debug("validator list: dropping embedded manifest for unlisted master",
			"master", hex.EncodeToString(masterKey[:]))
		return
	}
	_ = a.validatorManifests.ApplyManifest(parsed, manifest.Uncapped)
}

func (a *Aggregator) applyEmbeddedPendingManifestsLocked(s *publisherState, entries []pendingEmbeddedManifest) {
	if len(entries) == 0 || a.validatorManifests == nil {
		return
	}
	listed := make(map[[33]byte]struct{}, len(s.Validators)+8)
	for _, k := range s.Validators {
		listed[k] = struct{}{}
	}
	for pubMaster, ps := range a.state {
		if pubMaster == s.MasterKey {
			continue
		}
		for _, k := range ps.Validators {
			listed[k] = struct{}{}
		}
	}
	for _, entry := range entries {
		a.applyEmbeddedManifestRawLocked(listed, entry.Raw)
	}
}

// applyPendingLocked stores a future-dated blob in the publisher's
// Remaining queue and returns Pending vs KnownSequence per rippled
// ValidatorList.cpp:1414-1432:
//
//   - Pending: no MaxSequence yet, or sequence > MaxSequence, or
//     sequence is unknown AND validFrom precedes the current
//     MaxSequence entry's validFrom (out-of-order delivery).
//   - KnownSequence: re-arrival at a sequence already queued.
//
// The blob is only stored on the Pending branch; KnownSequence is a
// no-op since we already have the same sequence queued. Caller must
// hold a.mu. Does NOT emit OnChange — promotion drives the trusted-set
// update.
func (a *Aggregator) applyPendingLocked(s *publisherState, blob *blobJSON, signingKey [33]byte, validFrom, validUntil time.Time, siteURI string, version uint32, rawManifest, rawLocalManifest []byte, rawLocalManifestSet bool, rawBlob, rawSignature []byte) Disposition {
	known := false
	if s.MaxSequenceSet {
		if _, hit := s.Remaining[blob.Sequence]; hit {
			known = true
		} else if blob.Sequence <= s.MaxSequence {
			if maxEntry, ok := s.Remaining[s.MaxSequence]; !ok || !validFrom.Before(maxEntry.Effective) {
				known = true
			}
		}
	}
	if known {
		return KnownSequence
	}

	keys := a.extractValidatorKeys(blob, "validator list: skipping invalid validator entry in pending blob")

	if s.Remaining == nil {
		s.Remaining = make(map[uint32]*pendingList, 2)
	}
	s.Remaining[blob.Sequence] = &pendingList{
		Sequence:            blob.Sequence,
		Effective:           validFrom,
		EffectiveSet:        blob.EffectiveSet,
		Expiration:          validUntil,
		Validators:          keys,
		SiteURI:             siteURI,
		Version:             version,
		SigningKey:          signingKey,
		RawManifest:         append([]byte(nil), rawManifest...),
		RawLocalManifest:    append([]byte(nil), rawLocalManifest...),
		RawLocalManifestSet: rawLocalManifestSet,
		RawBlob:             append([]byte(nil), rawBlob...),
		RawSignature:        append([]byte(nil), rawSignature...),
		EmbeddedManifests:   a.pendingEmbeddedManifests(blob),
	}
	if version > s.Version {
		s.Version = version
	}
	if s.Version < 2 {
		s.Version = 2
	}
	s.RawManifest = append([]byte(nil), rawManifest...)
	a.recordMaxSequenceLocked(s, blob.Sequence)
	return Pending
}

func (a *Aggregator) pendingEmbeddedManifests(blob *blobJSON) []pendingEmbeddedManifest {
	var out []pendingEmbeddedManifest
	for _, entry := range blob.Validators {
		if entry.Manifest == "" {
			continue
		}
		if len(entry.Manifest) > manifest.MaxManifestBase64 {
			continue
		}
		raw, err := decodeBase64Tolerant([]byte(entry.Manifest))
		if err != nil {
			continue
		}
		if _, err := manifest.Deserialize(raw); err != nil {
			continue
		}
		out = append(out, pendingEmbeddedManifest{Raw: append([]byte(nil), raw...)})
	}
	return out
}

func (a *Aggregator) recordMaxSequenceLocked(s *publisherState, sequence uint32) {
	if !s.MaxSequenceSet || sequence > s.MaxSequence {
		s.MaxSequence = sequence
		s.MaxSequenceSet = true
	}
}

// normalizeRemainingLocked mirrors the cleanup at ValidatorList.cpp:1022-
// 1037. Remaining is ordered by sequence, but only entries whose effective
// times are strictly increasing can remain relevant. Caller must hold a.mu.
func (a *Aggregator) normalizeRemainingLocked(s *publisherState) {
	if s == nil || len(s.Remaining) == 0 {
		return
	}
	seqs := make([]uint32, 0, len(s.Remaining))
	for sequence := range s.Remaining {
		seqs = append(seqs, sequence)
	}
	slices.Sort(seqs)
	for i, sequence := range seqs {
		if sequence <= s.Sequence {
			delete(s.Remaining, sequence)
			continue
		}
		nextIndex := i + 1
		if nextIndex < len(seqs) {
			next := s.Remaining[seqs[nextIndex]]
			current := s.Remaining[sequence]
			if next != nil && current != nil && !next.Effective.After(current.Effective) {
				delete(s.Remaining, sequence)
			}
		}
	}
	if len(s.Remaining) == 0 {
		s.Remaining = nil
	}
}

// promotePendingSequenceLocked handles the applyList race where a queued
// sequence has become effective before the time-driven promotion sweep.
// Caller must hold a.mu and must have confirmed that sequence is in
// Remaining and greater than the current sequence.
func (a *Aggregator) promotePendingSequenceLocked(s *publisherState, sequence uint32, now time.Time) {
	chosen := s.Remaining[sequence]
	if chosen == nil || sequence <= s.Sequence {
		return
	}
	s.Sequence = chosen.Sequence
	s.Effective = chosen.Effective
	s.EffectiveSet = chosen.EffectiveSet
	s.Expiration = chosen.Expiration
	s.SigningKey = chosen.SigningKey
	s.SiteURI = chosen.SiteURI
	s.LastUpdate = now
	if chosen.Version > s.Version {
		s.Version = chosen.Version
	}
	s.updateRawBytes(chosen.RawManifest, chosen.RawBlob, chosen.RawSignature)
	s.updateRawLocalManifest(chosen.RawLocalManifest, chosen.RawLocalManifestSet)
	s.Validators = append([][33]byte(nil), chosen.Validators...)
	s.Status = StatusAvailable
	if !chosen.Expiration.After(now) {
		s.Status = StatusExpired
		s.Validators = nil
	} else {
		a.applyEmbeddedPendingManifestsLocked(s, chosen.EmbeddedManifests)
	}
	delete(s.Remaining, sequence)
	a.recordMaxSequenceLocked(s, sequence)
	a.normalizeRemainingLocked(s)
}

// promoteRemainingLocked rotates ready Remaining entries (those whose
// validFrom <= now) into the publisher's current slot, mirroring
// rippled's updateTrusted loop at ValidatorList.cpp:1929-1991.
// Operates on a single publisher; caller drives publisher iteration if
// promoting for the whole set.
//
// Walks Remaining in ascending sequence order so a chain of stacked
// rotations resolves to the LAST ready entry (rippled's iter/next
// scan). Earlier entries are skipped and discarded — rippled
// likewise erases [firstIter, std::next(iter)] after the rotation.
//
// Caller must hold a.mu. Does not emit OnChange — caller decides when
// to recompute (typically immediately after the call when invoked at
// ingest, or via Tick → recomputeAndEmitLocked on the time-driven
// path).
func (a *Aggregator) promoteRemainingLocked(s *publisherState, now time.Time) uint32 {
	a.normalizeRemainingLocked(s)
	if len(s.Remaining) == 0 {
		return 0
	}
	seqs := make([]uint32, 0, len(s.Remaining))
	for seq := range s.Remaining {
		seqs = append(seqs, seq)
	}
	slices.Sort(seqs)

	pickIdx := -1
	for i, seq := range seqs {
		p := s.Remaining[seq]
		if !p.Effective.After(now) {
			pickIdx = i
			continue
		}
		break
	}
	if pickIdx < 0 {
		return 0
	}

	chosen := s.Remaining[seqs[pickIdx]]
	a.promotePendingSequenceLocked(s, chosen.Sequence, now)

	// Erase all entries up to and including the chosen one — rippled
	// remaining.erase(firstIter, std::next(iter)).
	for i := 0; i <= pickIdx; i++ {
		delete(s.Remaining, seqs[i])
	}
	if len(s.Remaining) == 0 {
		s.Remaining = nil
	}
	a.normalizeRemainingLocked(s)
	a.writeCacheLocked(s)
	return chosen.Sequence
}

// ApplyCollection processes a v2 collection (TMValidatorListCollection),
// applying each blob individually with the collection's shared publisher
// manifest. Results preserve blob order and include the publisher key and
// highest observed sequence when extractable.
//
// Mirrors rippled's applyLists at ValidatorList.cpp:998-1070.
func (a *Aggregator) ApplyCollection(coll *message.ValidatorListCollection, siteURI string) ([]Disposition, PublisherKey, uint32) {
	return a.applyCollection(coll, siteURI, false)
}

func (a *Aggregator) applyCollectionFromSite(coll *message.ValidatorListCollection, siteURI string) ([]Disposition, PublisherKey, uint32) {
	return a.applyCollection(coll, siteURI, true)
}

func (a *Aggregator) applyCollection(coll *message.ValidatorListCollection, siteURI string, asyncDispatch bool) ([]Disposition, PublisherKey, uint32) {
	if coll == nil {
		return []Disposition{Malformed}, PublisherKey{}, 0
	}
	if !isSupportedVersion(coll.Version) {
		return []Disposition{UnsupportedVersion}, PublisherKey{}, 0
	}
	if len(coll.Blobs) == 0 {
		return []Disposition{Malformed}, PublisherKey{}, 0
	}
	// Anti-abuse cap. Matches rippled ValidatorList.h:272
	// `static constexpr std::size_t maxSupportedBlobs = 5;` enforced
	// at ValidatorList.cpp:428 (v2 JSON path) and 472-473 (parseBlobs).
	// A peer that sends a collection larger than this would force the
	// aggregator to run N signature verifications; reject before any
	// crypto work.
	if len(coll.Blobs) > maxSupportedBlobs {
		return []Disposition{Malformed}, PublisherKey{}, 0
	}
	out := make([]Disposition, len(coll.Blobs))
	var resultKey PublisherKey
	var resultSequence uint32
	var resultDisposition Disposition
	resultSet := false
	for i, blob := range coll.Blobs {
		// Per blob: prefer the embedded local manifest when present,
		// else fall back to the collection's shared manifest. Matches
		// rippled applyList(globalManifest, localManifest, ...) at
		// ValidatorList.cpp:1140-1151.
		disp, pk, sequence := a.applyListInternal(
			coll.Manifest,
			blob.Manifest,
			blob.HasManifest(),
			blob.Blob,
			blob.Signature,
			coll.Version,
			siteURI,
			asyncDispatch,
		)
		out[i] = disp
		if !resultSet || disp.Severity() < resultDisposition.Severity() ||
			(disp == resultDisposition && sequence > resultSequence) {
			resultDisposition = disp
			resultKey = pk
			resultSequence = sequence
			resultSet = true
		}
	}
	if resultKey != (PublisherKey{}) {
		a.mu.Lock()
		if state := a.state[resultKey]; state != nil && state.MaxSequenceSet {
			resultSequence = state.MaxSequence
		}
		a.mu.Unlock()
	}
	return out, resultKey, resultSequence
}
