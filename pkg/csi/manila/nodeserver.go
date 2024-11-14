/*
Copyright 2019 The Kubernetes Authors.

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

package manila

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/cloud-provider-openstack/pkg/util"
	"k8s.io/klog/v2"
)

type nodeServer struct {
	d *Driver
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	klog.Infof("Publish context: %+#v", req.GetPublishContext())

	// Forward the RPC
	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).PublishVolume(ctx, req)
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if err := validateNodeUnpublishVolumeRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).UnpublishVolume(ctx, req)
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	// pass cephfs secrets
	req.Secrets = util.SetMapIfNotEmpty(req.Secrets, "userID", req.VolumeContext["userID"])
	req.Secrets = util.SetMapIfNotEmpty(req.Secrets, "userKey", req.VolumeContext["userKey"])

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).StageVolume(ctx, req)
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if err := validateNodeUnstageVolumeRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).UnstageVolume(ctx, req)
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	nodeInfo := &csi.NodeGetInfoResponse{
		NodeId: ns.d.nodeID,
	}

	if ns.d.withTopology {
		nodeInfo.AccessibleTopology = &csi.Topology{
			Segments: map[string]string{topologyKey: ns.d.nodeAZ},
		}
	}

	return nodeInfo, nil
}

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: ns.d.nscaps,
	}, nil
}

func (ns *nodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).GetVolumeStats(ctx, req)
}

func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
