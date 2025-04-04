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
	"strings"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/shares"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/cloud-provider-openstack/pkg/client"
	"k8s.io/cloud-provider-openstack/pkg/csi/manila/options"
	"k8s.io/cloud-provider-openstack/pkg/csi/manila/shareadapters"
	clouderrors "k8s.io/cloud-provider-openstack/pkg/util/errors"
	"k8s.io/cloud-provider-openstack/pkg/util/metadata"
	"k8s.io/klog/v2"
)

type nodeServer struct {
	d *Driver

	metadata          metadata.IMetadata
	supportsNodeStage bool
	// The result of NodeStageVolume is stashed away for NodePublishVolume(s) that will follow
	nodeStageCache    map[volumeID]stageCacheEntry
	nodeStageCacheMtx sync.RWMutex
	csi.UnimplementedNodeServer
}

type stageCacheEntry struct {
	volumeContext map[string]string
	stageSecret   map[string]string
	publishSecret map[string]string
}

func (ns *nodeServer) buildVolumeContext(ctx context.Context, volID volumeID, shareOpts *options.NodeVolumeContext, osOpts *client.AuthOpts) (
	volumeContext map[string]string, accessRight *shares.AccessRight, err error,
) {
	manilaClient, err := ns.d.manilaClientBuilder.New(ctx, osOpts)
	if err != nil {
		return nil, nil, status.Errorf(codes.Unauthenticated, "failed to create Manila v2 client: %v", err)
	}

	// Retrieve the share by its ID or name

	var share *shares.Share

	if shareOpts.ShareID != "" {
		share, err = manilaClient.GetShareByID(ctx, shareOpts.ShareID)
		if err != nil {
			errCode := codes.Internal
			if clouderrors.IsNotFound(err) {
				errCode = codes.NotFound
			}

			return nil, nil, status.Errorf(errCode, "failed to retrieve volume with share ID %s: %v", shareOpts.ShareID, err)
		}
	} else {
		share, err = manilaClient.GetShareByName(ctx, shareOpts.ShareName)
		if err != nil {
			errCode := codes.Internal
			if clouderrors.IsNotFound(err) {
				errCode = codes.NotFound
			}

			return nil, nil, status.Errorf(errCode, "failed to retrieve volume with share name %s: %v", shareOpts.ShareName, err)
		}
	}

	// Verify the plugin supports this share

	if !strings.EqualFold(share.ShareProto, ns.d.shareProto) {
		return nil, nil, status.Errorf(codes.InvalidArgument,
			"wrong share protocol %s for volume %s, the plugin is set to operate in %s",
			share.ShareProto, volID, ns.d.shareProto)
	}

	if share.Status != shareAvailable {
		if share.Status == shareCreating {
			return nil, nil, status.Errorf(codes.Unavailable, "volume %s is in transient creating state", volID)
		}

		return nil, nil, status.Errorf(codes.FailedPrecondition, "invalid share status for volume %s: expected 'available', got '%s'",
			volID, share.Status)
	}

	// Get the access right for this share

	accessRights, err := manilaClient.GetAccessRights(ctx, share.ID)
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "failed to list access rights for volume %s: %v", volID, err)
	}

	for i := range accessRights {
		if accessRights[i].ID == shareOpts.ShareAccessID {
			accessRight = &accessRights[i]
			break
		}
	}

	if accessRight == nil {
		return nil, nil, status.Errorf(codes.InvalidArgument, "cannot find access right %s for volume %s",
			shareOpts.ShareAccessID, volID)
	}

	// Retrieve list of all export locations for this share.
	// Share adapter will try to choose the correct one for mounting.

	availableExportLocations, err := manilaClient.GetExportLocations(ctx, share.ID)
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "failed to list export locations for volume %s: %v", volID, err)
	}

	// Build volume context for fwd plugin

	sa := getShareAdapter(ns.d.shareProto)
	opts := &shareadapters.VolumeContextArgs{
		Locations: availableExportLocations,
		Options:   shareOpts,
	}
	volumeContext, err = sa.BuildVolumeContext(opts)
	if err != nil {
		return nil, nil, status.Errorf(codes.InvalidArgument, "failed to build volume context for volume %s: %v", volID, err)
	}

	return
}

func buildNodePublishSecret(accessRight *shares.AccessRight, sa shareadapters.ShareAdapter, volID volumeID) (map[string]string, error) {
	opts := &shareadapters.SecretArgs{
		AccessRight: accessRight,
	}
	secret, err := sa.BuildNodePublishSecret(opts)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to build publish secret for volume %s: %v", volID, err)
	}

	return secret, nil
}

func buildNodeStageSecret(accessRight *shares.AccessRight, sa shareadapters.ShareAdapter, volID volumeID) (map[string]string, error) {
	opts := &shareadapters.SecretArgs{
		AccessRight: accessRight,
	}
	secret, err := sa.BuildNodeStageSecret(opts)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to build stage secret for volume %s: %v", volID, err)
	}

	return secret, nil
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if err := validateNodePublishVolumeRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Configuration

	shareOpts, err := options.NewNodeVolumeContext(req.GetVolumeContext())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume context: %v", err)
	}

	osOpts, err := options.NewOpenstackOptions(req.GetSecrets())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid OpenStack secrets: %v", err)
	}

	volID := volumeID(req.GetVolumeId())

	var (
		accessRight       *shares.AccessRight
		volumeCtx, secret map[string]string
	)

	if ns.supportsNodeStage {
		// STAGE_UNSTAGE_VOLUME capability is enabled, NodeStageVolume should've already built the staging data

		ns.nodeStageCacheMtx.RLock()
		cacheEntry, ok := ns.nodeStageCache[volID]
		ns.nodeStageCacheMtx.RUnlock()

		if ok {
			volumeCtx, secret = cacheEntry.volumeContext, cacheEntry.publishSecret
		} else {
			klog.Warningf("STAGE_UNSTAGE_VOLUME capability is enabled, but node stage cache doesn't contain an entry for %s - this is most likely a bug! Rebuilding staging data anyway...", volID)
			volumeCtx, accessRight, err = ns.buildVolumeContext(ctx, volID, shareOpts, osOpts)
			if err == nil {
				secret, err = buildNodePublishSecret(accessRight, getShareAdapter(ns.d.shareProto), volID)
			}
		}
	} else {
		volumeCtx, accessRight, err = ns.buildVolumeContext(ctx, volID, shareOpts, osOpts)
		if err == nil {
			secret, err = buildNodePublishSecret(accessRight, getShareAdapter(ns.d.shareProto), volID)
		}
	}
	if err != nil {
		return nil, err
	}

	// Forward the RPC

	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	req.Secrets = secret
	req.VolumeContext = volumeCtx

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
	if err := validateNodeStageVolumeRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Configuration

	var (
		accessRight                *shares.AccessRight
		volumeCtx                  map[string]string
		stageSecret, publishSecret map[string]string
		err                        error
	)

	shareOpts, err := options.NewNodeVolumeContext(req.GetVolumeContext())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume context: %v", err)
	}

	osOpts, err := options.NewOpenstackOptions(req.GetSecrets())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid OpenStack secrets: %v", err)
	}

	volID := volumeID(req.GetVolumeId())

	ns.nodeStageCacheMtx.Lock()
	if cacheEntry, ok := ns.nodeStageCache[volID]; ok {
		volumeCtx, stageSecret = cacheEntry.volumeContext, cacheEntry.stageSecret
	} else {
		volumeCtx, accessRight, err = ns.buildVolumeContext(ctx, volID, shareOpts, osOpts)

		if err == nil {
			stageSecret, err = buildNodeStageSecret(accessRight, getShareAdapter(ns.d.shareProto), volID)
		}

		if err == nil {
			publishSecret, err = buildNodePublishSecret(accessRight, getShareAdapter(ns.d.shareProto), volID)
		}

		if err == nil {
			ns.nodeStageCache[volID] = stageCacheEntry{volumeContext: volumeCtx, stageSecret: stageSecret, publishSecret: publishSecret}
		}
	}
	ns.nodeStageCacheMtx.Unlock()
	if err != nil {
		return nil, err
	}

	// Forward the RPC

	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	req.Secrets = stageSecret
	req.VolumeContext = volumeCtx

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).StageVolume(ctx, req)
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if err := validateNodeUnstageVolumeRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	ns.nodeStageCacheMtx.Lock()
	delete(ns.nodeStageCache, volumeID(req.VolumeId))
	ns.nodeStageCacheMtx.Unlock()

	csiConn, err := ns.d.csiClientBuilder.NewConnectionWithContext(ctx, ns.d.fwdEndpoint)
	if err != nil {
		return nil, status.Error(codes.Unavailable, fmtGrpcConnError(ns.d.fwdEndpoint, err))
	}
	defer csiConn.Close()

	return ns.d.csiClientBuilder.NewNodeServiceClient(csiConn).UnstageVolume(ctx, req)
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	nodeID, err := ns.metadata.GetInstanceID()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "[NodeGetInfo] unable to retrieve instance id of node %v", err)
	}

	nodeInfo := &csi.NodeGetInfoResponse{
		NodeId: nodeID,
	}

	if !ns.d.withTopology {
		return nodeInfo, nil
	}

	zone, err := ns.metadata.GetAvailabilityZone()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "[NodeGetInfo] Unable to retrieve availability zone of node %v", err)
	}

	nodeInfo.AccessibleTopology = &csi.Topology{
		Segments: map[string]string{topologyKey: zone},
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
