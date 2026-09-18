package controller

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// plaintextCredentialsCondition reports env vars whose NAME looks like a
// credential but whose value is a literal in the CRD.
const plaintextCredentialsCondition = "PlaintextCredentials"

// credentialNamePattern is the name heuristic from CAI-155. Names only;
// a value heuristic misses every custom format and the name is what the
// author chose. PUBLIC/PUBLISHABLE names are the deliberate exception:
// a publishable key is meant to be public and a warning on it trains
// people to ignore the condition.
var credentialNamePattern = regexp.MustCompile(`(?i)(^|_)(KEY|SECRET|TOKEN|PASSWORD|PASSWD|WEBHOOK)(_|$)`)
var publicNamePattern = regexp.MustCompile(`(?i)(^|_)(PUBLIC|PUBLISHABLE)(_|$)`)

func looksLikeCredentialName(name string) bool {
	return credentialNamePattern.MatchString(name) && !publicNamePattern.MatchString(name)
}

// setPlaintextCredentialsCondition names credential-shaped env vars carried
// as literals. `value:` is the obvious shape and every example uses it, so
// credentials land in the CRD as plaintext by default and are then copied
// into every annotation and dump of the object. The condition steers the
// author to valueFrom.secretRef at the moment it is cheap to change.
func setPlaintextCredentialsCondition(conds *[]metav1.Condition, app *mortisev1alpha1.App, envs []mortisev1alpha1.Environment, generation int64) {
	var parts []string
	var shared []string
	for _, sv := range app.Spec.SharedVars {
		if sv.ValueFrom == nil && sv.Value != "" && looksLikeCredentialName(sv.Name) {
			shared = append(shared, sv.Name)
		}
	}
	if len(shared) > 0 {
		parts = append(parts, "sharedVars: "+strings.Join(shared, ", "))
	}
	for _, env := range envs {
		var names []string
		for _, ev := range env.Env {
			if ev.ValueFrom == nil && ev.Value != "" && looksLikeCredentialName(ev.Name) {
				names = append(names, ev.Name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, env.Name+": "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		meta.RemoveStatusCondition(conds, plaintextCredentialsCondition)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               plaintextCredentialsCondition,
		Status:             metav1.ConditionFalse,
		Reason:             "LiteralLooksLikeCredential",
		Message:            fmt.Sprintf("these variables are named like credentials but carried as literals in the App spec (%s); move each into a Secret and reference it with valueFrom.secretRef. Names only; values are not inspected", strings.Join(parts, "; ")),
		ObservedGeneration: generation,
	})
}
