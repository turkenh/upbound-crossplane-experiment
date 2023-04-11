/*
Copyright 2023 The Crossplane Authors.

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

// Package composition contains internal logic linked to the validation of the v1.Composition type.
package composition

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/crossplane/crossplane-runtime/pkg/controller"
	xperrors "github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/resource/unstructured"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/internal/features"
	"github.com/crossplane/crossplane/pkg/validation/apiextensions/v1/composition"
)

// handler implements the admission handler for Composition.
type handler struct {
	reader  client.Reader
	decoder *admission.Decoder
	options controller.Options
}

// InjectDecoder injects the decoder.
func (h *handler) InjectDecoder(decoder *admission.Decoder) error {
	h.decoder = decoder
	return nil
}

// SetupWebhookWithManager sets up the webhook with the manager.
func SetupWebhookWithManager(mgr ctrl.Manager, options controller.Options) error {
	if options.Features.Enabled(features.EnableAlphaCompositionWebhookSchemaValidation) {
		indexer := mgr.GetFieldIndexer()
		if err := indexer.IndexField(context.Background(), &extv1.CustomResourceDefinition{}, "spec.group", func(obj client.Object) []string {
			return []string{obj.(*extv1.CustomResourceDefinition).Spec.Group}
		}); err != nil {
			return err
		}
		if err := indexer.IndexField(context.Background(), &extv1.CustomResourceDefinition{}, "spec.names.kind", func(obj client.Object) []string {
			return []string{obj.(*extv1.CustomResourceDefinition).Spec.Names.Kind}
		}); err != nil {
			return err
		}
	}

	// TODO(lsviben): switch to using admission.CustomValidator when https://github.com/kubernetes-sigs/controller-runtime/issues/1896 is resolved.
	mgr.GetWebhookServer().Register(v1.CompositionValidatingWebhookPath,
		&webhook.Admission{Handler: &handler{
			reader:  unstructured.NewClient(mgr.GetClient()),
			options: options,
		}})

	return nil
}

// Handle handles the admission request, validating the Composition.
func (h *handler) Handle(ctx context.Context, request admission.Request) admission.Response {
	switch request.Operation {
	case admissionv1.Create, admissionv1.Update:
		c := &v1.Composition{}
		if err := h.decoder.Decode(request, c); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		warns, err := h.Validate(ctx, c)
		if err == nil {
			return admission.Allowed("").WithWarnings(warns...)
		}
		var apiStatus apierrors.APIStatus
		if errors.As(err, &apiStatus) {
			return validationResponseFromStatus(false, apiStatus.Status()).WithWarnings(warns...)
		}
		return admission.Denied(err.Error()).WithWarnings(warns...)
	case admissionv1.Delete:
		return admission.Allowed("")
	case admissionv1.Connect:
		return admission.Errored(http.StatusBadRequest, errors.New("unexpected operation"))
	default:
		return admission.Errored(http.StatusBadRequest, errors.New("unexpected operation"))
	}
}

func validationResponseFromStatus(allowed bool, status metav1.Status) admission.Response {
	resp := admission.Response{
		AdmissionResponse: admissionv1.AdmissionResponse{
			Allowed: allowed,
			Result:  &status,
		},
	}
	return resp
}

// Validate validates the Composition by rendering it and then validating the rendered resources.
//
//nolint:gocyclo //TODO(phisco): refactor if possible
func (h *handler) Validate(ctx context.Context, comp *v1.Composition) (warns []string, err error) {
	// Validate the composition itself, we'll disable it on the Validator below
	var validationErrs field.ErrorList
	warns, validationErrs = comp.Validate()
	if len(validationErrs) != 0 {
		return warns, apierrors.NewInvalid(comp.GroupVersionKind().GroupKind(), comp.GetName(), validationErrs)
	}

	if !h.options.Features.Enabled(features.EnableAlphaCompositionWebhookSchemaValidation) {
		return warns, nil
	}

	// Get the composition validation mode from annotation
	validationMode, err := comp.GetValidationMode()
	if err != nil {
		return warns, xperrors.Wrap(err, "cannot get validation mode")
	}

	// Get all the needed CRDs, Composite Resource, Managed resources ... ? Error out if missing in strict mode
	gvkToCRD, errs := h.getNeededCRDs(ctx, comp)
	var shouldSkip bool
	for _, err := range errs {
		if err == nil {
			continue
		}
		// If any of the errors is not a NotFound error, error out
		if !apierrors.IsNotFound(err) {
			return warns, xperrors.Errorf("there were some errors while getting the needed CRDs: %v", errs)
		}
		// If any of the needed CRDs is not found, error out if strict mode is enabled
		switch validationMode {
		case v1.CompositionValidationModeStrict:
			return warns, xperrors.Wrap(err, "cannot get needed CRDs and strict mode is enabled")
		case v1.CompositionValidationModeLoose:
			// Given that some requirement is missing, and we are in loose mode, skip validation
			shouldSkip = true
			warns = append(warns, fmt.Sprintf("cannot get needed CRDs and loose mode is enabled: %v", err))
			continue
		}
	}

	if shouldSkip {
		return warns, nil
	}

	v, err := composition.NewValidator(
		composition.WithCRDGetterFromMap(gvkToCRD),
		// We disable logical Validation as this has already been done above
		composition.WithoutLogicalValidation(),
	)
	if err != nil {
		return warns, apierrors.NewInternalError(err)
	}
	schemaWarns, errList := v.Validate(ctx, comp)
	warns = append(warns, schemaWarns...)
	if len(errList) != 0 {
		return warns, apierrors.NewInvalid(comp.GroupVersionKind().GroupKind(), comp.GetName(), errList)
	}
	return warns, nil
}

func (h *handler) getNeededCRDs(ctx context.Context, comp *v1.Composition) (map[schema.GroupVersionKind]apiextensions.CustomResourceDefinition, []error) {
	var resultErrs []error
	neededCrds := make(map[schema.GroupVersionKind]apiextensions.CustomResourceDefinition)

	// Get schema for the Composite Resource Definition defined by comp.Spec.CompositeTypeRef
	compositeResGVK := schema.FromAPIVersionAndKind(comp.Spec.CompositeTypeRef.APIVersion,
		comp.Spec.CompositeTypeRef.Kind)

	compositeCRD, err := h.getCRDForGVK(ctx, &compositeResGVK)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, []error{err}
		}
		resultErrs = append(resultErrs, err)
	}
	if compositeCRD != nil {
		neededCrds[compositeResGVK] = *compositeCRD
	}

	// Get schema for all Managed Resource Definitions defined by comp.Spec.Resources
	for _, res := range comp.Spec.Resources {
		cd, err := res.GetBaseObject()
		if err != nil {
			return nil, []error{err}
		}
		gvk := cd.GetObjectKind().GroupVersionKind()
		if _, ok := neededCrds[gvk]; ok {
			continue
		}
		crd, err := h.getCRDForGVK(ctx, &gvk)
		switch {
		case apierrors.IsNotFound(err):
			resultErrs = append(resultErrs, err)
		case err != nil:
			return nil, []error{err}
		case compositeCRD != nil:
			neededCrds[gvk] = *crd
		}
	}

	return neededCrds, resultErrs
}

// getCRDForGVK returns the validation schema for the given GVK, by looking up the CRD by group and kind using
// the provided client.
func (h *handler) getCRDForGVK(ctx context.Context, gvk *schema.GroupVersionKind) (*apiextensions.CustomResourceDefinition, error) {
	crds := extv1.CustomResourceDefinitionList{}
	if err := h.reader.List(ctx, &crds,
		client.MatchingFields{"spec.group": gvk.Group},
		client.MatchingFields{"spec.names.kind": gvk.Kind}); err != nil {
		return nil, err
	}
	if len(crds.Items) != 1 {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "CustomResourceDefinition"}, fmt.Sprintf("%s.%s", gvk.Kind, gvk.Group))
	}
	crd := crds.Items[0]
	internal := &apiextensions.CustomResourceDefinition{}
	return internal, extv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&crd, internal, nil)
}
