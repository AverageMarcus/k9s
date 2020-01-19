package xray

import (
	"context"
	"fmt"
	"strconv"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/render"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/node"
)

type Pod struct{}

func (p *Pod) Status(po *v1.Pod) {

}

func (p *Pod) Render(ctx context.Context, ns string, o interface{}) error {
	pwm, ok := o.(*render.PodWithMetrics)
	if !ok {
		return fmt.Errorf("Expected PodWithMetrics, but got %T", o)
	}

	var po v1.Pod
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(pwm.Raw.Object, &po)
	if err != nil {
		return err
	}

	f, ok := ctx.Value(internal.KeyFactory).(dao.Factory)
	if !ok {
		return fmt.Errorf("no factory found in context")
	}

	phase := p.phase(&po)
	ss := po.Status.ContainerStatuses
	cr, _, _ := p.statuses(ss)
	status := OkStatus
	if cr != len(ss) {
		status = ToastStatus
	}
	if phase == "Completed" {
		status = CompletedStatus
	}

	node := NewTreeNode("v1/pods", client.FQN(po.Namespace, po.Name))
	node.Extras[StatusKey] = status
	node.Extras[StateKey] = strconv.Itoa(cr) + "/" + strconv.Itoa(len(ss))
	parent, ok := ctx.Value(KeyParent).(*TreeNode)
	if !ok {
		return fmt.Errorf("Expecting a TreeNode but got %T", ctx.Value(KeyParent))
	}
	parent.Add(node)

	ctx = context.WithValue(ctx, KeyParent, node)
	var cre Container
	for i := 0; i < len(po.Spec.InitContainers); i++ {
		if err := cre.Render(ctx, ns, render.ContainerRes{Container: &po.Spec.InitContainers[i]}); err != nil {
			return err
		}
	}
	for i := 0; i < len(po.Spec.Containers); i++ {
		if err := cre.Render(ctx, ns, render.ContainerRes{Container: &po.Spec.Containers[i]}); err != nil {
			return err
		}
	}
	p.podVolumeRefs(f, node, po.Namespace, po.Spec.Volumes)

	return nil
}

func (*Pod) podVolumeRefs(f dao.Factory, parent *TreeNode, ns string, vv []v1.Volume) {
	for _, v := range vv {
		sec := v.VolumeSource.Secret
		if sec != nil {
			addRef(f, parent, "v1/secrets", client.FQN(ns, sec.SecretName), nil)
			continue
		}

		cm := v.VolumeSource.ConfigMap
		if cm != nil {
			addRef(f, parent, "v1/configmaps", client.FQN(ns, cm.LocalObjectReference.Name), nil)
			continue
		}

		pvc := v.VolumeSource.PersistentVolumeClaim
		if pvc != nil {
			addRef(f, parent, "v1/persistentvolumeclaims", client.FQN(ns, pvc.ClaimName), nil)
		}
	}
}

// BOZO!! Dedup...
func (*Pod) statuses(ss []v1.ContainerStatus) (cr, ct, rc int) {
	for _, c := range ss {
		if c.State.Terminated != nil {
			ct++
		}
		if c.Ready {
			cr = cr + 1
		}
		rc += int(c.RestartCount)
	}

	return
}

func (p *Pod) phase(po *v1.Pod) string {
	status := string(po.Status.Phase)
	if po.Status.Reason != "" {
		if po.DeletionTimestamp != nil && po.Status.Reason == node.NodeUnreachablePodReason {
			return "Unknown"
		}
		status = po.Status.Reason
	}

	status, ok := p.initContainerPhase(po.Status, len(po.Spec.InitContainers), status)
	if ok {
		return status
	}

	status, ok = p.containerPhase(po.Status, status)
	if ok && status == "Completed" {
		status = "Running"
	}
	if po.DeletionTimestamp == nil {
		return status
	}

	return "Terminated"
}

func (*Pod) containerPhase(st v1.PodStatus, status string) (string, bool) {
	var running bool
	for i := len(st.ContainerStatuses) - 1; i >= 0; i-- {
		cs := st.ContainerStatuses[i]
		switch {
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
			status = cs.State.Waiting.Reason
		case cs.State.Terminated != nil && cs.State.Terminated.Reason != "":
			status = cs.State.Terminated.Reason
		case cs.State.Terminated != nil:
			if cs.State.Terminated.Signal != 0 {
				status = "Signal:" + strconv.Itoa(int(cs.State.Terminated.Signal))
			} else {
				status = "ExitCode:" + strconv.Itoa(int(cs.State.Terminated.ExitCode))
			}
		case cs.Ready && cs.State.Running != nil:
			running = true
		}
	}

	return status, running
}

func (p *Pod) initContainerPhase(st v1.PodStatus, initCount int, status string) (string, bool) {
	for i, cs := range st.InitContainerStatuses {
		s := checkContainerStatus(cs, i, initCount)
		if s == "" {
			continue
		}
		return s, true
	}

	return status, false
}

func checkContainerStatus(cs v1.ContainerStatus, i, initCount int) string {
	switch {
	case cs.State.Terminated != nil:
		if cs.State.Terminated.ExitCode == 0 {
			return ""
		}
		if cs.State.Terminated.Reason != "" {
			return "Init:" + cs.State.Terminated.Reason
		}
		if cs.State.Terminated.Signal != 0 {
			return "Init:Signal:" + strconv.Itoa(int(cs.State.Terminated.Signal))
		}
		return "Init:ExitCode:" + strconv.Itoa(int(cs.State.Terminated.ExitCode))
	case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
		return "Init:" + cs.State.Waiting.Reason
	default:
		return "Init:" + strconv.Itoa(i) + "/" + strconv.Itoa(initCount)
	}
}