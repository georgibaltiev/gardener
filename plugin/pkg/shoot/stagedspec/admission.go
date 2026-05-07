// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package stagedspec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/admission"

	"github.com/gardener/gardener/pkg/apis/core"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	plugin "github.com/gardener/gardener/plugin/pkg"
)

// Register registers a plugin.
func Register(plugins *admission.Plugins) {
	plugins.Register(plugin.PluginNameShootStagedSpec, func(_ io.Reader) (admission.Interface, error) {
		return New(), nil
	})
}

// StagedSpec contains the admission handler.
type StagedSpec struct {
	*admission.Handler
}

// New creates a new StagedSpec admission plugin.
func New() *StagedSpec {
	return &StagedSpec{
		Handler: admission.NewHandler(admission.Update),
	}
}

var _ admission.ValidationInterface = (*StagedSpec)(nil)

// Validate rejects Shoot spec updates when confineSpecUpdateRollout is enabled,
// unless only .spec.hibernation.enabled changed or the request comes from a gardenlet.
func (s *StagedSpec) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetKind().GroupKind() != core.Kind("Shoot") {
		return nil
	}

	if a.GetSubresource() != "" {
		return nil
	}

	newShoot, ok := a.GetObject().(*core.Shoot)
	if !ok {
		return apierrors.NewInternalError(errors.New("could not convert resource into Shoot object"))
	}

	oldShoot, ok := a.GetOldObject().(*core.Shoot)
	if !ok {
		return apierrors.NewInternalError(errors.New("could not convert old resource into Shoot object"))
	}

	if !isConfineSpecUpdateRolloutEnabled(oldShoot) {
		return nil
	}

	if apiequality.Semantic.DeepEqual(oldShoot.Spec, newShoot.Spec) {
		return nil
	}

	if onlyAllowedFieldsChanged(&oldShoot.Spec, &newShoot.Spec) {
		return nil
	}

	if isInternalActor(a) {
		return nil
	}

	return admission.NewForbidden(a, fmt.Errorf(
		"spec changes are not allowed when confineSpecUpdateRollout is enabled; "+
			"write your desired spec into ConfigMap 'shoot-%s-staged-spec' in namespace %q",
		a.GetName(), a.GetNamespace(),
	))
}

func isConfineSpecUpdateRolloutEnabled(shoot *core.Shoot) bool {
	return shoot.Spec.Maintenance != nil &&
		shoot.Spec.Maintenance.ConfineSpecUpdateRollout != nil &&
		*shoot.Spec.Maintenance.ConfineSpecUpdateRollout
}

func onlyAllowedFieldsChanged(oldSpec, newSpec *core.ShootSpec) bool {
	oldCopy := oldSpec.DeepCopy()
	newCopy := newSpec.DeepCopy()

	// Normalize fields that are always allowed to change
	normalizeAllowedFields(oldCopy)
	normalizeAllowedFields(newCopy)

	return apiequality.Semantic.DeepEqual(oldCopy, newCopy)
}

func normalizeAllowedFields(spec *core.ShootSpec) {
	// Hibernation.Enabled changes are always allowed (immediate rollout exception)
	if spec.Hibernation == nil {
		spec.Hibernation = &core.Hibernation{}
	}
	spec.Hibernation.Enabled = nil

	// Maintenance changes are always allowed (administrative, not spec-rollout)
	spec.Maintenance = nil
}

func isInternalActor(a admission.Attributes) bool {
	userInfo := a.GetUserInfo()
	if userInfo == nil {
		return false
	}

	if slices.Contains(userInfo.GetGroups(), v1beta1constants.SeedsGroup) {
		return true
	}

	if strings.HasPrefix(userInfo.GetName(), v1beta1constants.SeedUserNamePrefix) {
		return true
	}

	// Allow service accounts (internal controllers like gardener-controller-manager)
	return strings.HasPrefix(userInfo.GetName(), "system:serviceaccount:")
}
