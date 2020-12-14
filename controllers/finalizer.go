package controllers

import (
	"context"
	"github.com/go-logr/logr"
	v1 "sidecar-hpa/api/v1"
	"sidecar-hpa/controllers/util"
)

const shpaFinalizer = "finalizer.dbisshpa.com"

func (r *SHPAReconciler) handleFinalizer(reqLogger logr.Logger, shpa *v1.SHPA) (bool, error) {
	// Check if the SHPA instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isSHPAMarkedToBeDeleted := shpa.GetDeletionTimestamp() != nil
	if isSHPAMarkedToBeDeleted {
		if util.ContainsString(shpa.GetFinalizers(), shpaFinalizer) {
			// Run finalization logic for shpaFinalizer. If the
			// finalization logic fails, don't remove the finalizer so
			// that we can retry during the next reconciliation
			r.finalizeSHPA(reqLogger, shpa)

			// Remove shpaFinalizer. Once all finalizers have been
			// removed, the object will be deleted
			shpa.SetFinalizers(util.RemoveString(shpa.GetFinalizers(), shpaFinalizer))
			err := r.client.Update(context.TODO(), shpa)
			if err != nil {
				return true, err
			}

		}
		return true, nil
	}

	// Add finalizer for this CR
	if !util.ContainsString(shpa.GetFinalizers(), shpaFinalizer) {
		if err := r.addFinalizer(reqLogger, shpa); err != nil {
			return true, nil
		}
	}

	return false, nil
}

func (r *SHPAReconciler) finalizeSHPA(reqLogger logr.Logger, shpa *v1.SHPA) {
	reqLogger.Info("Successfully finalized SHPA")
}

func (r *SHPAReconciler) addFinalizer(reqLogger logr.Logger, shpa *v1.SHPA) error {
	reqLogger.Info("Adding Finalizer for the SHPA")
	shpa.SetFinalizers(append(shpa.GetFinalizers(), shpaFinalizer))

	// Update CR
	err := r.client.Update(context.TODO(), shpa)
	if err != nil {
		reqLogger.Error(err, "Failed to update SHPA with finalizer")
		return err
	}

	return nil
}
