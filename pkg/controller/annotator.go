package controller

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/luthermonson/fargo/pkg/reconcile"
)

// Annotation keys written back onto watched objects.
const (
	AnnotationTxn     = "fargo.gascity.dev/txn"
	AnnotationOutcome = "fargo.gascity.dev/outcome"
)

// annotateWriter records the transaction id and outcome as annotations on the
// watched object. It is the only write argo/flux modes make to the management
// cluster.
type annotateWriter struct {
	client dynamic.Interface
}

func (w *annotateWriter) Record(ctx context.Context, ref reconcile.ObjRef, id string, outcome reconcile.Outcome) error {
	gvr := schema.GroupVersionResource{Group: ref.GVRGroup, Version: ref.GVRVersion, Resource: ref.GVRResource}
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				AnnotationTxn:     id,
				AnnotationOutcome: string(outcome),
			},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = w.client.Resource(gvr).Namespace(ref.Namespace).Patch(
		ctx, ref.Name, types.MergePatchType, data, metav1.PatchOptions{FieldManager: "fargo"})
	if err != nil {
		return fmt.Errorf("annotate %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return nil
}

// statusWriter updates the Transaction CRD status for crd mode.
type statusWriter struct {
	client dynamic.Interface
}

func outcomeToPhase(o reconcile.Outcome) string {
	switch o {
	case reconcile.OutcomeOpen:
		return "Open"
	case reconcile.OutcomeCommitted:
		return "Committed"
	case reconcile.OutcomeRolledBack:
		return "RolledBack"
	default:
		return "Pending"
	}
}

func (w *statusWriter) Record(ctx context.Context, ref reconcile.ObjRef, id string, outcome reconcile.Outcome) error {
	gvr := schema.GroupVersionResource{Group: ref.GVRGroup, Version: ref.GVRVersion, Resource: ref.GVRResource}
	ri := w.client.Resource(gvr).Namespace(ref.Namespace)
	cur, err := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get transaction %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	obj := cur.DeepCopy()
	if obj.Object["status"] == nil {
		obj.Object["status"] = map[string]any{}
	}
	_ = unstructured.SetNestedField(obj.Object, outcomeToPhase(outcome), "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, id, "status", "txnId")
	if _, err := ri.UpdateStatus(ctx, obj, metav1.UpdateOptions{FieldManager: "fargo"}); err != nil {
		return fmt.Errorf("update transaction status %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return nil
}
