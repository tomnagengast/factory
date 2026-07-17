package runs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// NewMigrationSnapshotReceipt derives the complete body-free receipt from the
// converted initial projection. Input ordering is irrelevant; duplicate or
// invalid source identities are rejected by the returned receipt validation.
func NewMigrationSnapshotReceipt(
	migrationID string,
	sourceRootDigest string,
	lifetimeRuns uint64,
	batches []AdmissionBatch,
	runs []Run,
	rateBuckets []RateBucket,
) (*MigrationSnapshotReceipt, error) {
	batches = cloneAdmissionBatches(batches)
	for index := range batches {
		canonicalizeAdmissionBatch(&batches[index])
		if err := validateAdmissionBatch(batches[index]); err != nil {
			return nil, fmt.Errorf("runs: migration admission batch %d: %w", index+1, err)
		}
	}
	slices.SortFunc(batches, compareAdmissionBatches)

	runs = cloneRuns(runs)
	for index := range runs {
		canonicalizeRun(&runs[index])
		if err := validateRun(runs[index], true); err != nil {
			return nil, fmt.Errorf("runs: migration Run %d: %w", index+1, err)
		}
	}
	slices.SortFunc(runs, compareRuns)

	rateBuckets = slices.Clone(rateBuckets)
	for index := range rateBuckets {
		rateBuckets[index].Minute = rateBuckets[index].Minute.UTC().Truncate(time.Minute)
	}
	slices.SortFunc(rateBuckets, compareRateBuckets)

	receipt := &MigrationSnapshotReceipt{
		Origin:           MigrationSnapshotOrigin,
		MigrationID:      migrationID,
		SourceRootDigest: sourceRootDigest,
		AdmissionBatches: cloneAdmissionBatches(batches),
		LifetimeRuns:     lifetimeRuns,
		RetainedBatches:  uint64(len(batches)),
		RateBucketCount:  uint64(len(rateBuckets)),
	}
	for _, batch := range batches {
		receipt.BatchIDs = append(receipt.BatchIDs, batch.ID)
		receipt.EventIDs = append(receipt.EventIDs, batch.EventID)
		if batch.EventSequence != 0 {
			receipt.EventSequences = append(receipt.EventSequences, batch.EventSequence)
		}
	}
	for _, run := range runs {
		receipt.RunIDs = append(receipt.RunIDs, run.ID)
		receipt.AdmissionIDs = append(receipt.AdmissionIDs, run.Causation.AdmissionID)
	}
	slices.Sort(receipt.BatchIDs)
	slices.Sort(receipt.EventIDs)
	slices.Sort(receipt.EventSequences)
	slices.Sort(receipt.RunIDs)
	slices.Sort(receipt.AdmissionIDs)

	var err error
	receipt.RateBucketDigest, err = canonicalRateBucketsDigest(rateBuckets)
	if err != nil {
		return nil, err
	}
	receipt.CanonicalRunsDigest, err = canonicalRunsDigest(runs)
	if err != nil {
		return nil, err
	}
	receipt.OperationID = migrationSnapshotOperationID(*receipt)
	if err := validateMigrationSnapshotReceiptShape(*receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func migrationSnapshotOperationID(receipt MigrationSnapshotReceipt) string {
	admissionBatches, _ := json.Marshal(receipt.AdmissionBatches)
	parts := []string{
		"factory-runs-migration-snapshot-v1",
		receipt.Origin,
		receipt.MigrationID,
		receipt.SourceRootDigest,
		"admission_batches",
		string(admissionBatches),
		"batch_ids",
	}
	parts = append(parts, receipt.BatchIDs...)
	parts = append(parts, "event_ids")
	parts = append(parts, receipt.EventIDs...)
	parts = append(parts, "event_sequences")
	for _, sequence := range receipt.EventSequences {
		parts = append(parts, strconv.FormatUint(sequence, 10))
	}
	parts = append(parts, "run_ids")
	parts = append(parts, receipt.RunIDs...)
	parts = append(parts, "admission_ids")
	parts = append(parts, receipt.AdmissionIDs...)
	parts = append(parts,
		"lifetime_runs", strconv.FormatUint(receipt.LifetimeRuns, 10),
		"retained_batches", strconv.FormatUint(receipt.RetainedBatches, 10),
		"rate_bucket_digest", receipt.RateBucketDigest,
		"rate_bucket_count", strconv.FormatUint(receipt.RateBucketCount, 10),
		"canonical_runs_digest", receipt.CanonicalRunsDigest,
	)
	return digestMigrationParts(parts...)
}

func canonicalRunsDigest(runs []Run) (string, error) {
	canonical := cloneRuns(runs)
	for index := range canonical {
		canonicalizeRun(&canonical[index])
	}
	slices.SortFunc(canonical, compareRuns)
	if len(canonical) == 0 {
		canonical = nil
	}
	return digestMigrationJSON("factory-runs-migration-runs-v1", canonical)
}

func canonicalRateBucketsDigest(rateBuckets []RateBucket) (string, error) {
	canonical := slices.Clone(rateBuckets)
	for index := range canonical {
		canonical[index].Minute = canonical[index].Minute.UTC().Truncate(time.Minute)
	}
	slices.SortFunc(canonical, compareRateBuckets)
	if len(canonical) == 0 {
		canonical = nil
	}
	return digestMigrationJSON("factory-runs-migration-rates-v1", canonical)
}

func digestMigrationJSON(domain string, value any) (string, error) {
	data, err := json.Marshal(struct {
		Domain string `json:"domain"`
		Value  any    `json:"value"`
	}{Domain: domain, Value: value})
	if err != nil {
		return "", fmt.Errorf("runs: encode migration digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func digestMigrationParts(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
