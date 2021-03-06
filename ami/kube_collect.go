package ami

import (
	"github.com/baetyl/baetyl-go/errors"
	"github.com/baetyl/baetyl-go/log"
	specv1 "github.com/baetyl/baetyl-go/spec/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/reference"
	"k8s.io/kubectl/pkg/scheme"
)

func (k *kubeImpl) CollectNodeInfo() (*specv1.NodeInfo, error) {
	node, err := k.cli.core.Nodes().Get(k.knn, metav1.GetOptions{})
	if err != nil {
		return nil, errors.Trace(err)
	}
	ni := node.Status.NodeInfo
	nodeInfo := &specv1.NodeInfo{
		Arch:             ni.Architecture,
		KernelVersion:    ni.KernelVersion,
		OS:               ni.OperatingSystem,
		ContainerRuntime: ni.ContainerRuntimeVersion,
		MachineID:        ni.MachineID,
		OSImage:          ni.OSImage,
		BootID:           ni.BootID,
		SystemUUID:       ni.SystemUUID,
	}
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			nodeInfo.Address = addr.Address
		} else if addr.Type == corev1.NodeHostName {
			nodeInfo.Hostname = addr.Address
		}
	}
	return nodeInfo, nil
}

func (k *kubeImpl) CollectNodeStats() (*specv1.NodeStats, error) {
	node, err := k.cli.core.Nodes().Get(k.knn, metav1.GetOptions{})
	if err != nil {
		return nil, errors.Trace(err)
	}
	nodeStats := &specv1.NodeStats{
		Usage:    map[string]string{},
		Capacity: map[string]string{},
	}
	nodeMetric, err := k.cli.metrics.NodeMetricses().Get(k.knn, metav1.GetOptions{})
	if err != nil {
		return nil, errors.Trace(err)
	}
	for res, quan := range nodeMetric.Usage {
		nodeStats.Usage[string(res)] = quan.String()
	}
	for res, quan := range node.Status.Capacity {
		if _, ok := nodeStats.Usage[string(res)]; ok {
			nodeStats.Capacity[string(res)] = quan.String()
		}
	}
	return nodeStats, nil
}

func (k *kubeImpl) CollectAppStats(ns string) ([]specv1.AppStats, error) {
	deploys, err := k.cli.app.Deployments(ns).List(metav1.ListOptions{})
	if err != nil {
		return nil, errors.Trace(err)
	}
	appStats := map[string]specv1.AppStats{}
	for _, deploy := range deploys.Items {
		appName := deploy.Labels[AppName]
		appVersion := deploy.Labels[AppVersion]
		serviceName := deploy.Labels[ServiceName]
		if appName == "" || serviceName == "" {
			continue
		}
		stats, ok := appStats[appName]
		if !ok {
			stats = specv1.AppStats{AppInfo: specv1.AppInfo{Name: appName, Version: appVersion}}
		}
		selector := labels.SelectorFromSet(deploy.Spec.Selector.MatchLabels)
		pods, err := k.cli.core.Pods(ns).List(metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return nil, errors.Trace(err)
		}
		for _, pod := range pods.Items {
			if stats.InstanceStats == nil {
				stats.InstanceStats = map[string]specv1.InstanceStats{}
			}
			stats.InstanceStats[pod.Name] = k.collectInstanceStats(ns, serviceName, &pod)
		}
		if pods == nil || len(pods.Items) == 0 {
			continue
		}
		stats.Status = getDeployStatus(stats.InstanceStats)
		appStats[appName] = stats
	}
	var res []specv1.AppStats
	for _, stats := range appStats {
		res = append(res, stats)
	}
	return res, nil
}

func getDeployStatus(infos map[string]specv1.InstanceStats) specv1.Status {
	var pending = false
	for _, info := range infos {
		if info.Status == specv1.Pending {
			pending = true
		} else if info.Status == specv1.Failed {
			return info.Status
		}
	}
	if pending {
		return specv1.Pending
	}
	return specv1.Running
}

func (k *kubeImpl) collectInstanceStats(ns, serviceName string, pod *corev1.Pod) specv1.InstanceStats {
	stats := specv1.InstanceStats{Name: pod.Name, ServiceName: serviceName, Usage: map[string]string{}}
	ref, err := reference.GetReference(scheme.Scheme, pod)
	if err != nil {
		k.log.Warn("failed to get service reference", log.Error(err))
		return stats
	}
	events, _ := k.cli.core.Events(ns).Search(scheme.Scheme, ref)
	if l := len(events.Items); l > 0 {
		if e := events.Items[l-1]; e.Type == "Warning" {
			stats.Cause += e.Message
		}
	}
	stats.CreateTime = pod.CreationTimestamp.Local()
	stats.Status = specv1.Status(pod.Status.Phase)
	for _, st := range pod.Status.ContainerStatuses {
		if st.State.Waiting != nil {
			stats.Status = specv1.Status(corev1.PodPending)
		}
	}
	podMetric, err := k.cli.metrics.PodMetricses(ns).Get(pod.Name, metav1.GetOptions{})
	if err != nil {
		k.log.Warn("failed to collect pod metrics", log.Error(err))
		return stats
	}
	for _, cont := range podMetric.Containers {
		if cont.Name == serviceName {
			for res, quan := range cont.Usage {
				stats.Usage[string(res)] = quan.String()
			}
		}
	}
	return stats
}
