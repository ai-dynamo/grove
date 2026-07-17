package podgangmap_new

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildBootstrapEntries emits the initial PodGangMap entries for a fresh PCS replica. It produces
// one anchor entry (epoch E0, DependsOn nil) carrying every standalone PCLQ's full replica count
// and every PCSG's MinAvailable replicas, plus one non-anchor entry per PCSG collecting that PCSG's
// replicas above MinAvailable (epoch E1 > E0, DependsOn E0). Returns the anchor entry first, the
// non-anchor entries after.
func buildBootstrapEntries(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, clk clock.Clock) []grovecorev1alpha1.PodGangEntry {
	hash := *pcs.Status.CurrentGenerationHash
	mpgEpoch := strconv.FormatInt(clk.Now().UnixNano(), 10)
	tpgEpoch := strconv.FormatInt(clk.Now().UnixNano()+1, 10)

	mpg := buildBootstrapMPG(pcs, pcsReplicaIndex, hash, mpgEpoch)
	entries := []grovecorev1alpha1.PodGangEntry{mpg}
	entries = append(entries, buildBootstrapTPGs(pcs, pcsReplicaIndex, hash, tpgEpoch, mpgEpoch)...)
	return entries
}

// buildBootstrapMPG returns the anchor entry carrying every standalone PCLQ's full Replicas count
// and every PCSG's MinAvailable replicas (PCSG indices [0, MinAvailable)). DependsOn is nil.
func buildBootstrapMPG(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, hash, epoch string) grovecorev1alpha1.PodGangEntry {
	entry := newPodGangEntry(pcs.Name, pcsReplicaIndex, epoch, hash, epoch, nil)
	entry.IsEpochAnchor = true
	entry.PodCliques = componentutils.GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs)

	pcsgMinAvailable := componentutils.GetPCSGMinAvailableFromPCSTemplateSpec(pcs)
	entry.PCSGReplicaIndices = make(map[string][]int32, len(pcsgMinAvailable))
	for name, minAvailable := range pcsgMinAvailable {
		indices := make([]int32, 0, minAvailable)
		for i := int32(0); i < minAvailable; i++ {
			indices = append(indices, i)
		}
		entry.PCSGReplicaIndices[name] = indices
	}
	return entry
}

// buildBootstrapTPGs returns one non-anchor entry per PCSG. Each entry collects that PCSG's replica
// indices at or above MinAvailable into a single PodGangEntry, shares the non-anchor batch epoch,
// and depends on mpgEpoch. Entries carry salted names so they stay distinct within the shared
// epoch. The PodGang materializer expands each entry into one PodGang per index.
func buildBootstrapTPGs(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, hash, epoch, mpgEpoch string) []grovecorev1alpha1.PodGangEntry {
	pcsgReplicas := componentutils.GetPCSGReplicasFromPCSTemplateSpec(pcs)
	pcsgMinAvailable := componentutils.GetPCSGMinAvailableFromPCSTemplateSpec(pcs)

	var tpgs []grovecorev1alpha1.PodGangEntry
	salt := 0
	for name, total := range pcsgReplicas {
		if total <= pcsgMinAvailable[name] {
			continue
		}
		indices := lo.RangeFrom(pcsgMinAvailable[name], int(total-pcsgMinAvailable[name]))
		entry := newPodGangEntry(pcs.Name, pcsReplicaIndex, fmt.Sprintf("%s-%d", epoch, salt), hash, epoch, []string{mpgEpoch})
		entry.PCSGReplicaIndices = map[string][]int32{name: indices}
		tpgs = append(tpgs, entry)
		salt++
	}
	return tpgs
}

// buildEntriesFromPCLQAndPCSGStatuses rebuilds the steady-state PodGangMap entries from the
// PodGangMapping status fields on the standalone PCLQs and PCSGs.
// It returns an ErrCodeContinueReconcileAndRequeue error when at least one standalone PCLQ or PCSG
// has not yet published its PodGangMapping status, signalling the caller to requeue rather than
// persist a partial rebuild. For existing entries their epoch, DependsOn, and IsAnchor are
// preserved. For new entries (created as a result of scale-out of PCSGs) a new epoch is created and
// its DependsOn is set to nil. Entries that end up with no PCLQ or PCSG content are dropped.
func buildEntriesFromPCLQAndPCSGStatuses(pcs *grovecorev1alpha1.PodCliqueSet,
	existingStandalonePCLQs []grovecorev1alpha1.PodClique,
	existingPCSGs []grovecorev1alpha1.PodCliqueScalingGroup,
	existingPGM *grovecorev1alpha1.PodGangMap,
	pcsReplicaIndex int,
	clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, error) {

	if !canRebuildPGMFromStatuses(pcs, existingStandalonePCLQs, existingPCSGs) {
		return nil, groveerr.New(groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("cannot rebuild PodGangMap for replica %d or PodCliqueSet %v: PodGangMapping for one or more PCLQ/PCSG are not yet published", pcsReplicaIndex, client.ObjectKeyFromObject(pcs)))
	}

	// preserve epoch, DependsOn, and IsAnchor for the existing PGM entries keyed by PodGang name.
	// For a new entry a fresh epoch, a nil DependsOn, and IsAnchor false will be set. These fields
	// are decided when an entry is first created and must survive a rebuild from owner statuses, so
	// they are preserved by PodGang name rather than recomputed.
	type preservedEntryFields struct {
		epoch     string
		dependsOn []string
		isAnchor  bool
	}
	existingByPGName := make(map[string]preservedEntryFields, len(existingPGM.Spec.Entries))
	for _, entry := range existingPGM.Spec.Entries {
		existingByPGName[entry.Name] = preservedEntryFields{
			epoch:     entry.Labels[apicommon.LabelEpoch],
			dependsOn: entry.DependsOn,
			isAnchor:  entry.IsEpochAnchor,
		}
	}

	// newEntryEpoch is new epoch shared by all new entries created as a result of PCSG scale-out.
	newEntryEpoch := strconv.FormatInt(clk.Now().UnixNano(), 10)
	entryByName := make(map[string]*grovecorev1alpha1.PodGangEntry)
	getOrCreateEntry := func(pgName string) *grovecorev1alpha1.PodGangEntry {
		if entry, ok := entryByName[pgName]; ok {
			return entry
		}
		var (
			epoch     string
			dependsOn []string
			isAnchor  bool
		)
		if preserved, isExisting := existingByPGName[pgName]; isExisting {
			epoch = preserved.epoch
			dependsOn = preserved.dependsOn
			isAnchor = preserved.isAnchor
		} else {
			epoch = newEntryEpoch
		}
		entry := newPodGangEntryWithName(pgName, *pcs.Status.CurrentGenerationHash, epoch, dependsOn)
		entry.IsEpochAnchor = isAnchor
		entryByName[pgName] = &entry
		return &entry
	}

	// sync PGM entries from standalone PCLQ.Status.PodGangMapping
	for i := range existingStandalonePCLQs {
		cliqueName, err := utils.GetPodCliqueNameFromPodCliqueFQN(existingStandalonePCLQs[i].ObjectMeta)
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeExtractPodCliqueName, component.OperationSync,
				fmt.Sprintf("Error extracting PodClique name from FQN %s", existingStandalonePCLQs[i].Name))
		}
		for pgName, podCount := range existingStandalonePCLQs[i].Status.PodGangMapping {
			entry := getOrCreateEntry(pgName)
			if entry.PodCliques == nil {
				entry.PodCliques = make(map[string]int32)
			}
			entry.PodCliques[cliqueName] = podCount
		}
	}

	// sync PGM entries from PCSG.Status.PodGangMapping
	for i := range existingPCSGs {
		pcsgConfigName := apicommon.ExtractScalingGroupNameFromPCSGFQN(existingPCSGs[i].Name, apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex})
		for pgName, replicaIndices := range existingPCSGs[i].Status.PodGangMapping {
			entry := getOrCreateEntry(pgName)
			if entry.PCSGReplicaIndices == nil {
				entry.PCSGReplicaIndices = make(map[string][]int32)
			}
			entry.PCSGReplicaIndices[pcsgConfigName] = replicaIndices
		}
	}
	entries := make([]grovecorev1alpha1.PodGangEntry, 0, len(entryByName))
	for _, entry := range entryByName {
		entries = append(entries, *entry)
	}
	return removeEmptyEntries(entries), nil
}

// canRebuildPGMFromStatuses returns true when every standalone PCLQ and every PCSG that the PCS
// spec declares is observed and has a non-empty Status.PodGangMapping. Rebuilding the PodGangMap
// from a partial set of owner statuses would wipe entries seeded from spec, so the rebuild waits
// until every owner has published its mapping.
func canRebuildPGMFromStatuses(pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) bool {
	if len(standalonePCLQs) < componentutils.CountStandalonePCLQs(pcs) {
		return false
	}
	if len(pcsgs) < len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	for _, pclq := range standalonePCLQs {
		if len(pclq.Status.PodGangMapping) == 0 {
			return false
		}
	}
	for _, pcsg := range pcsgs {
		if len(pcsg.Status.PodGangMapping) == 0 {
			return false
		}
	}
	return true
}

// removeEmptyEntries drops entries whose PodClique pod counts are all zero and whose PCSG index
// slices are all empty. These arise from a scale-in where a PodGang's membership drained to zero.
func removeEmptyEntries(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntry {
	return slices.DeleteFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		for _, count := range entry.PodCliques {
			if count > 0 {
				return false
			}
		}
		for _, indices := range entry.PCSGReplicaIndices {
			if len(indices) > 0 {
				return false
			}
		}
		return true
	})
}

// reconstructEntriesFromExistingPodGangs rebuilds PodGangMap entries from live BPG/SPG PodGangs
// on upgrade from a pre-coherent Grove version (see the reconstruction case in section 11.4 of
// the design). It assigns epoch E0 to the BPG (the entry with no grove.io/base-podgang label) with
// DependsOn nil, and epoch E1 > E0 to each SPG (an entry carrying the grove.io/base-podgang label)
// with DependsOn = &E0, so a gang-termination recreate keeps the BPG-first-then-SPG scheduling
// order. The BPG is identified by the absence of the base-podgang label rather than by carrying
// standalone PodClique pods, so a PCS whose cliques are all PCSG-owned (empty PodCliques on the
// BPG) still gets exactly one anchor. Returns an error if a PodGang's PodGroup names cannot be
// parsed.
func reconstructEntriesFromExistingPodGangs(pcs *grovecorev1alpha1.PodCliqueSet, existingPGs []groveschedulerv1alpha1.PodGang, pcsReplicaIndex int, clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, error) {
	var (
		pcsGenerationHash = *pcs.Status.CurrentGenerationHash
		bpgEpoch          = strconv.FormatInt(clk.Now().UnixNano(), 10)
		spgEpoch          = strconv.FormatInt(clk.Now().UnixNano()+1, 10)
	)

	pgEntries := make([]grovecorev1alpha1.PodGangEntry, 0, len(existingPGs))
	for i := range existingPGs {
		pgEntry, err := buildEntryFromPodGang(pcs, pcsReplicaIndex, pcsGenerationHash, existingPGs[i])
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeReconstructPodGangMapEntry,
				component.OperationSync,
				fmt.Sprintf("Error reconstructing PodGangMap entry from PodGang %s for PodCliqueSet: %v", existingPGs[i].Name, client.ObjectKeyFromObject(pcs)),
			)
		}
		if _, isSPG := existingPGs[i].Labels[apicommon.LabelBasePodGang]; isSPG {
			// A scaled PodGang carries the base-podgang label pointing at its BPG. It depends on
			// the BPG and is not an anchor.
			pgEntry.Labels = map[string]string{apicommon.LabelEpoch: spgEpoch}
			pgEntry.DependsOn = []string{bpgEpoch}
		} else {
			// The base PodGang carries no base-podgang label. It is the anchor.
			pgEntry.IsEpochAnchor = true
			pgEntry.Labels = map[string]string{apicommon.LabelEpoch: bpgEpoch}
		}
		pgEntries = append(pgEntries, *pgEntry)
	}
	return pgEntries, nil
}

// buildEntryFromPodGang reconstructs a PodGangEntry from a live PodGang. Each PodGroup name is a
// PodClique FQN: a standalone PodClique group contributes its pod count (number of pod references)
// to PodCliques, while a PCSG-owned group contributes the PCSG replica index parsed from the FQN
// to PCSGReplicaIndices. The same PCSG replica appears once per constituent clique, so indices are
// de-duplicated per PCSG. Epoch and DependsOn are left unset; the caller assigns them.
func buildEntryFromPodGang(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsGenerationHash string, pg groveschedulerv1alpha1.PodGang) (*grovecorev1alpha1.PodGangEntry, error) {
	pgmEntry := &grovecorev1alpha1.PodGangEntry{
		Name:                       pg.Name,
		PodCliqueSetGenerationHash: pcsGenerationHash,
		PodCliques:                 make(map[string]int32),
		PCSGReplicaIndices:         make(map[string][]int32),
	}

	pcsgNameToIndexSet := make(map[string]sets.Set[int32])
	for _, podGroup := range pg.Spec.PodGroups {
		cliqueName, err := extractCliqueName(pcs, podGroup.Name)
		if err != nil {
			return nil, err
		}
		pcsgConfig := componentutils.FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueName)
		if pcsgConfig == nil {
			// Clique is a standalone PCLQ - pod count = number of pod references
			pgmEntry.PodCliques[cliqueName] = int32(len(podGroup.PodReferences))
			continue
		}
		// PCSG-owned PodGroup: extract the PCSG replica index from PCSG FQN
		pcsgIndex, err := extractPCSGReplicaIndexFromPCLQFQN(podGroup.Name, pcs.Name, pcsReplicaIndex, pcsgConfig.Name, cliqueName)
		if err != nil {
			return nil, err
		}
		if pcsgNameToIndexSet[pcsgConfig.Name] == nil {
			pcsgNameToIndexSet[pcsgConfig.Name] = sets.New[int32]()
		}
		pcsgNameToIndexSet[pcsgConfig.Name].Insert(pcsgIndex)
	}
	for pcsgName, indexSet := range pcsgNameToIndexSet {
		pgmEntry.PCSGReplicaIndices[pcsgName] = sets.List(indexSet)
	}

	return pgmEntry, nil
}

// extractCliqueName returns the unqualified clique template name for a PodClique FQN by matching
// the FQN's trailing segment against the clique templates declared in the PCS spec. Works from a
// bare FQN string (it does not need the PodClique object's labels). Returns an error if no
// template matches.
func extractCliqueName(pcs *grovecorev1alpha1.PodCliqueSet, pclqFQN string) (string, error) {
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if cliqueTemplate == nil {
			continue
		}
		suffix := "-" + cliqueTemplate.Name
		if len(pclqFQN) > len(suffix) && pclqFQN[len(pclqFQN)-len(suffix):] == suffix {
			return cliqueTemplate.Name, nil
		}
	}
	return "", fmt.Errorf("PodGroup name %q does not match any known clique template in PCS %s", pclqFQN, pcs.Name)
}

// extractPCSGReplicaIndexFromPCLQFQN parses the PCSG replica index from a PCSG-owned PodClique FQN
// of the form <pcsgFQN>-<pcsgReplicaIndex>-<cliqueName>, where pcsgFQN is derived from pcsName,
// pcsReplicaIndex, and pcsgName. Returns an error if the FQN does not match that shape. Grove is
// the sole writer of these names, so a parse failure is a contract violation, not a soft skip.
func extractPCSGReplicaIndexFromPCLQFQN(pclqFQN string, pcsName string, pcsReplicaIndex int, pcsgName, cliqueName string) (int32, error) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex}
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, pcsgName)
	prefix := pcsgFQN + "-"
	suffix := "-" + cliqueName
	if !strings.HasPrefix(pclqFQN, prefix) || !strings.HasSuffix(pclqFQN, suffix) {
		return 0, fmt.Errorf("PCLQ FQN %q does not match expected shape %s<index>%s", pclqFQN, prefix, suffix)
	}
	mid := pclqFQN[len(prefix) : len(pclqFQN)-len(suffix)]
	pcsgIndex, err := strconv.Atoi(mid)
	if err != nil {
		return 0, fmt.Errorf("PCSG replica index in PCLQ FQN %q is not a valid integer: %w", pclqFQN, err)
	}
	return int32(pcsgIndex), nil
}
