/*
Copyright 2026 nrx-ops.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package adminapi

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// Labels and keys of the credentials routes.
const (
	// labelCreatedBy marks the Secrets the dashboard created. It is not
	// LabelManagedBy on purpose: that label marks the per-Job Secrets the
	// controller mints, which the dashboard hides.
	labelCreatedBy   = "app.kubernetes.io/created-by"
	createdByValue   = "stellarcd-dashboard"
	keyGCSCredential = "GOOGLE_CREDENTIALS"
	keyVaultToken    = "VAULT_TOKEN"
)

// Credential kinds, inferred from the keys of a Secret.
const (
	credentialKindGitHubApp = "GitHubApp"
	credentialKindSSHKey    = "SSHKey"
	credentialKindToken     = "Token"
	credentialKindBasicAuth = "BasicAuth"
	credentialKindGCS       = "GCS"
	credentialKindVault     = "Vault"
	credentialKindGeneric   = "Generic"
)

// Roles a Secret plays for the object that references it.
const (
	credentialRoleGit       = "Git"
	credentialRoleState     = "State"
	credentialRoleDiscovery = "Discovery"
	credentialRoleEnv       = "Env"
)

// Messages of the credentials routes.
const (
	msgCredentialsNamespace = "Credentials can only be managed in the namespace of a Universe."
	msgSecretNotFound       = "This Secret does not exist in this namespace."
	msgSecretExists         = "A Secret with this name already exists"
	msgSecretNoKey          = "Add at least one key"
)

// CredentialInfo describes a Secret without any of its values.
type CredentialInfo struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Keys      []string        `json:"keys"`
	CreatedAt string          `json:"createdAt"`
	UsedBy    []CredentialUse `json:"usedBy"`
}

// CredentialUse is one object referencing a Secret, and what for.
type CredentialUse struct {
	// Kind is "Galaxy" or "Astral".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Role is "Git", "State", "Discovery" or "Env".
	Role string `json:"role"`
}

// CredentialRequest creates a Secret. Data maps key names to plain values.
type CredentialRequest struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
}

// CredentialDataRequest replaces the whole data of a Secret.
type CredentialDataRequest struct {
	Data map[string]string `json:"data"`
}

// handleListCredentials lists the Secrets of a Universe namespace with their
// key names and the objects using them. Values never leave this handler.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.credentialsNamespace(w, r)
	if !ok {
		return
	}
	var list corev1.SecretList
	if err := s.Reader.List(r.Context(), &list, client.InNamespace(namespace)); err != nil {
		s.writeError(w, r, err, "Failed to list the Secrets of this namespace.")
		return
	}
	uses, err := s.credentialUses(r.Context(), namespace)
	if err != nil {
		s.writeError(w, r, err, "Failed to find what uses the Secrets of this namespace.")
		return
	}

	out := make([]CredentialInfo, 0, len(list.Items))
	for i := range list.Items {
		if !listedSecret(&list.Items[i]) {
			continue
		}
		out = append(out, credentialInfo(&list.Items[i], uses))
	}
	slices.SortFunc(out, func(a, b CredentialInfo) int { return cmp.Compare(a.Name, b.Name) })
	s.writeJSON(w, r, out)
}

// handleCreateCredential creates an Opaque Secret from plain values.
func (s *Server) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.credentialsNamespace(w, r)
	if !ok {
		return
	}
	var req CredentialRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	if errs := validation.IsDNS1123Subdomain(req.Name); len(errs) > 0 {
		s.fail(w, r, http.StatusBadRequest, "name",
			"Use at most 253 lowercase letters, digits, '-' or '.', starting and ending with a letter or digit")
		return
	}
	if msg := validateCredentialData(req.Data); msg != "" {
		s.fail(w, r, http.StatusBadRequest, "data", msg)
		return
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: namespace,
			Labels:    map[string]string{labelCreatedBy: createdByValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: secretData(req.Data),
	}
	if err := s.Client.Create(r.Context(), secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			s.fail(w, r, http.StatusConflict, "name", msgSecretExists)
			return
		}
		s.writeError(w, r, err, "Failed to create the Secret.")
		return
	}
	s.logWrite("create-secret", secret.Namespace, secret.Name)
	// A Secret that did not exist a moment ago has no user yet.
	s.writeStatus(w, r, http.StatusCreated, credentialInfo(secret, nil))
}

// handleUpdateCredential replaces the whole data of a Secret, keeping its
// labels and annotations.
func (s *Server) handleUpdateCredential(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.credentialsNamespace(w, r)
	if !ok {
		return
	}
	var req CredentialDataRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	if msg := validateCredentialData(req.Data); msg != "" {
		s.fail(w, r, http.StatusBadRequest, "data", msg)
		return
	}

	key := client.ObjectKey{Namespace: namespace, Name: r.PathValue("name")}
	var secret *corev1.Secret
	// The read and the write are not atomic, so a Secret changed in between
	// is read again once: the new data replaces everything anyway.
	for attempt := 0; ; attempt++ {
		var found bool
		if secret, found = s.readCredential(w, r, key); !found {
			return
		}
		secret.Data = secretData(req.Data)
		secret.StringData = nil
		err := s.Client.Update(r.Context(), secret)
		if err == nil {
			break
		}
		if apierrors.IsConflict(err) && attempt == 0 {
			continue
		}
		s.writeError(w, r, err, "Failed to save the Secret.")
		return
	}
	s.logWrite("update-secret", secret.Namespace, secret.Name)

	uses, err := s.credentialUses(r.Context(), namespace)
	if err != nil {
		s.writeError(w, r, err, "The Secret was saved, but what uses it could not be listed.")
		return
	}
	s.writeJSON(w, r, credentialInfo(secret, uses))
}

// handleDeleteCredential deletes a Secret nothing references.
func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.credentialsNamespace(w, r)
	if !ok {
		return
	}
	secret, found := s.readCredential(w, r, client.ObjectKey{Namespace: namespace, Name: r.PathValue("name")})
	if !found {
		return
	}
	uses, err := s.credentialUses(r.Context(), namespace)
	if err != nil {
		s.writeError(w, r, err, "Failed to find what uses this Secret.")
		return
	}
	if users := uses[secret.Name]; len(users) > 0 {
		s.fail(w, r, http.StatusConflict, "", secretInUseMessage(users))
		return
	}
	if err := s.Client.Delete(r.Context(), secret); err != nil {
		s.writeError(w, r, err, "Failed to delete the Secret.")
		return
	}
	s.logWrite("delete-secret", secret.Namespace, secret.Name)
	w.WriteHeader(http.StatusNoContent)
}

// credentialsNamespace reads the {namespace} path parameter and checks that a
// Universe owns it: the dashboard manages credentials of tenants only, never
// of arbitrary namespaces such as the operator's own. It answers the request
// and returns false when the namespace is refused.
func (s *Server) credentialsNamespace(w http.ResponseWriter, r *http.Request) (string, bool) {
	namespace := r.PathValue("namespace")
	var universes corev1alpha1.UniverseList
	if err := s.Reader.List(r.Context(), &universes); err != nil {
		s.writeError(w, r, err, "Failed to list Universes.")
		return "", false
	}
	for i := range universes.Items {
		if universes.Items[i].Status.Namespace == namespace {
			return namespace, true
		}
	}
	s.fail(w, r, http.StatusForbidden, "", msgCredentialsNamespace)
	return "", false
}

// readCredential reads a Secret the dashboard may manage. Secrets it hides are
// answered as missing. It answers the request and returns false on failure.
func (s *Server) readCredential(w http.ResponseWriter, r *http.Request, key client.ObjectKey) (*corev1.Secret, bool) {
	var secret corev1.Secret
	if err := s.Reader.Get(r.Context(), key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			s.fail(w, r, http.StatusNotFound, "", msgSecretNotFound)
			return nil, false
		}
		s.writeError(w, r, err, "This Secret could not be read.")
		return nil, false
	}
	if !listedSecret(&secret) {
		s.fail(w, r, http.StatusNotFound, "", msgSecretNotFound)
		return nil, false
	}
	return &secret, true
}

// listedSecret reports whether the dashboard may show and manage a Secret.
// ServiceAccount tokens and Helm releases are never credentials, and the
// per-Job Secrets the controller mints (a GitHub App installation token, for
// instance) expire with their Job.
func listedSecret(secret *corev1.Secret) bool {
	return !slices.Contains(hiddenSecretTypes, secret.Type) &&
		secret.Labels[corev1alpha1.LabelManagedBy] != corev1alpha1.ManagedByValue
}

// credentialUses maps each Secret name of a namespace to the Galaxies and
// Astrals referencing it, sorted by kind, name and role.
func (s *Server) credentialUses(ctx context.Context, namespace string) (map[string][]CredentialUse, error) {
	var galaxies corev1alpha1.GalaxyList
	if err := s.Reader.List(ctx, &galaxies, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list galaxies: %w", err)
	}
	var astrals corev1alpha1.AstralList
	if err := s.Reader.List(ctx, &astrals, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list astrals: %w", err)
	}

	uses := map[string][]CredentialUse{}
	add := func(ref *corev1alpha1.SecretRef, kind, name, role string) {
		if ref != nil && ref.Name != "" {
			uses[ref.Name] = append(uses[ref.Name], CredentialUse{Kind: kind, Name: name, Role: role})
		}
	}
	for i := range galaxies.Items {
		g := &galaxies.Items[i]
		add(g.Spec.VCSSecretRef, kindGalaxy, g.Name, credentialRoleGit)
		if g.Spec.BackendConfig != nil {
			add(g.Spec.BackendConfig.SecretRef, kindGalaxy, g.Name, credentialRoleState)
		}
		if g.Spec.Discovery != nil {
			add(g.Spec.Discovery.Repository.SecretRef, kindGalaxy, g.Name, credentialRoleDiscovery)
		}
	}
	for i := range astrals.Items {
		a := &astrals.Items[i]
		add(a.Spec.VCSRepository.SecretRef, kindAstral, a.Name, credentialRoleGit)
		add(a.Spec.BackendSecretRef, kindAstral, a.Name, credentialRoleState)
		for _, ref := range a.Spec.Secrets {
			add(&corev1alpha1.SecretRef{Name: ref.Name}, kindAstral, a.Name, credentialRoleEnv)
		}
	}

	for name, list := range uses {
		slices.SortFunc(list, func(a, b CredentialUse) int {
			return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Role, b.Role))
		})
		uses[name] = slices.Compact(list)
	}
	return uses, nil
}

// credentialInfo describes a Secret by its key names only.
func credentialInfo(secret *corev1.Secret, uses map[string][]CredentialUse) CredentialInfo {
	usedBy := uses[secret.Name]
	if usedBy == nil {
		usedBy = []CredentialUse{}
	}
	created := ""
	if !secret.CreationTimestamp.IsZero() {
		created = secret.CreationTimestamp.UTC().Format(time.RFC3339)
	}
	return CredentialInfo{
		Name:      secret.Name,
		Kind:      credentialKind(secret.Data),
		Keys:      slices.Sorted(maps.Keys(secret.Data)),
		CreatedAt: created,
		UsedBy:    usedBy,
	}
}

// credentialKind infers what a Secret holds from its keys. The Git layouts
// follow the precedence the controller applies when it clones.
func credentialKind(data map[string][]byte) string {
	switch shared.DetectAuthMethod(data) {
	case shared.AuthMethodGitHubApp:
		return credentialKindGitHubApp
	case shared.AuthMethodSSHKey:
		return credentialKindSSHKey
	case shared.AuthMethodToken:
		return credentialKindToken
	case shared.AuthMethodBasicAuth:
		return credentialKindBasicAuth
	}
	if len(data[keyGCSCredential]) > 0 {
		return credentialKindGCS
	}
	if len(data[keyVaultToken]) > 0 {
		return credentialKindVault
	}
	return credentialKindGeneric
}

// validateCredentialData checks the keys and values of a Secret to write. It
// returns a message, or "" when the data is acceptable. Messages name keys,
// never values.
func validateCredentialData(data map[string]string) string {
	if len(data) == 0 {
		return msgSecretNoKey
	}
	for _, key := range slices.Sorted(maps.Keys(data)) {
		if errs := validation.IsConfigMapKey(key); len(errs) > 0 {
			return fmt.Sprintf("Invalid key %s: use letters, digits, '-', '_' or '.'", key)
		}
		if data[key] == "" {
			return fmt.Sprintf("The value of %s is empty", key)
		}
	}
	return ""
}

// secretData converts plain values into Secret data.
func secretData(data map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(data))
	for k, v := range data {
		out[k] = []byte(v)
	}
	return out
}

// secretInUseMessage lists the objects keeping a Secret from being deleted.
func secretInUseMessage(users []CredentialUse) string {
	parts := make([]string, 0, len(users))
	for _, u := range users {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", u.Kind, u.Name, u.Role))
	}
	return "This Secret is used by: " + strings.Join(parts, ", ") + ". Remove it from them first."
}
