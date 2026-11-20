package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Image 對應 GET /images/json 的項目子集。
type Image struct {
	ID         string   `json:"Id"`
	RepoTags   []string `json:"RepoTags"`
	Created    int64    `json:"Created"`
	Size       int64    `json:"Size"`
	Containers int      `json:"Containers"` // 使用中的容器數；-1 表示 daemon 未計算
}

// ShortID 去掉 sha256: 前綴後取 12 碼。
func (i Image) ShortID() string {
	id := strings.TrimPrefix(i.ID, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

// Name 回傳第一個 repo:tag；無標籤（dangling）為 "<none>"。
func (i Image) Name() string {
	if len(i.RepoTags) == 0 || i.RepoTags[0] == "<none>:<none>" {
		return "<none>"
	}
	return i.RepoTags[0]
}

// Volume 對應 GET /volumes 的項目子集。
type Volume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	Scope      string            `json:"Scope"`
}

// Network 對應 GET /networks 的項目子集。
type Network struct {
	ID         string                     `json:"Id"`
	Name       string                     `json:"Name"`
	Created    string                     `json:"Created"`
	Scope      string                     `json:"Scope"`
	Driver     string                     `json:"Driver"`
	Internal   bool                       `json:"Internal"`
	Labels     map[string]string          `json:"Labels"`
	Containers map[string]json.RawMessage `json:"Containers"`
}

// ContainerCount 回傳已連接容器數（列表 API 常不帶 Containers，此時為 0）。
func (n Network) ContainerCount() int { return len(n.Containers) }

// Builtin 判斷是否為 Docker 內建網路（bridge／host／none 不可刪除）。
func (n Network) Builtin() bool {
	switch n.Name {
	case "bridge", "host", "none":
		return true
	}
	return false
}

// PruneReport 為 prune 結果摘要。
type PruneReport struct {
	Count          int   `json:"count"`
	SpaceReclaimed int64 `json:"space_reclaimed"`
}

// ListImages GET /images/json。
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	resp, err := c.do(ctx, http.MethodGet, "/images/json", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v []Image
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	return v, nil
}

// RemoveImage DELETE /images/{ref}。使用中的映像回 409 APIError（呼叫端決定是否 force）。
func (c *Client) RemoveImage(ctx context.Context, ref string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	resp, err := c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(ref), q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PruneImages POST /images/prune，移除所有未被容器使用的映像（含有標籤者）。
func (c *Client) PruneImages(ctx context.Context) (PruneReport, error) {
	q := url.Values{}
	q.Set("filters", `{"dangling":["false"]}`)
	resp, err := c.doWith(ctx, c.longHC, http.MethodPost, "/images/prune", q, nil)
	if err != nil {
		return PruneReport{}, err
	}
	defer resp.Body.Close()
	var v struct {
		ImagesDeleted  []map[string]string `json:"ImagesDeleted"`
		SpaceReclaimed int64               `json:"SpaceReclaimed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return PruneReport{}, fmt.Errorf("decode prune: %w", err)
	}
	n := 0
	for _, d := range v.ImagesDeleted {
		if d["Deleted"] != "" {
			n++
		}
	}
	return PruneReport{Count: n, SpaceReclaimed: v.SpaceReclaimed}, nil
}

// ListVolumes GET /volumes。
func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	resp, err := c.do(ctx, http.MethodGet, "/volumes", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Volumes []Volume `json:"Volumes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode volumes: %w", err)
	}
	return v.Volumes, nil
}

// RemoveVolume DELETE /volumes/{name}。使用中回 409 APIError。
func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	resp, err := c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PruneVolumes POST /volumes/prune（all=true：連具名 volume 一起，只要沒有容器使用）。
func (c *Client) PruneVolumes(ctx context.Context) (PruneReport, error) {
	q := url.Values{}
	q.Set("filters", `{"all":["true"]}`)
	resp, err := c.doWith(ctx, c.longHC, http.MethodPost, "/volumes/prune", q, nil)
	if err != nil {
		return PruneReport{}, err
	}
	defer resp.Body.Close()
	var v struct {
		VolumesDeleted []string `json:"VolumesDeleted"`
		SpaceReclaimed int64    `json:"SpaceReclaimed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return PruneReport{}, fmt.Errorf("decode prune: %w", err)
	}
	return PruneReport{Count: len(v.VolumesDeleted), SpaceReclaimed: v.SpaceReclaimed}, nil
}

// ListNetworks GET /networks。
func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	resp, err := c.do(ctx, http.MethodGet, "/networks", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v []Network
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	return v, nil
}

// RemoveNetwork DELETE /networks/{id|name}。內建或使用中回 APIError（常見 403）。
func (c *Client) RemoveNetwork(ctx context.Context, idOrName string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(idOrName), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PruneNetworks POST /networks/prune（移除未被容器使用的自訂網路）。
func (c *Client) PruneNetworks(ctx context.Context) (PruneReport, error) {
	resp, err := c.doWith(ctx, c.longHC, http.MethodPost, "/networks/prune", nil, nil)
	if err != nil {
		return PruneReport{}, err
	}
	defer resp.Body.Close()
	var v struct {
		NetworksDeleted []string `json:"NetworksDeleted"`
		SpaceReclaimed  int64    `json:"SpaceReclaimed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return PruneReport{}, fmt.Errorf("decode prune: %w", err)
	}
	return PruneReport{Count: len(v.NetworksDeleted), SpaceReclaimed: v.SpaceReclaimed}, nil
}

// ---- disk usage（GET /system/df，等同 docker system df）----------------------

// DFImage 是 /system/df Images 的項目子集。
type DFImage struct {
	ID         string `json:"Id"`
	Size       int64  `json:"Size"`
	SharedSize int64  `json:"SharedSize"`
	Containers int    `json:"Containers"`
}

// DFContainer 是 /system/df Containers 的項目子集（SizeRw＝可寫層）。
type DFContainer struct {
	ID         string   `json:"Id"`
	Names      []string `json:"Names"`
	SizeRw     int64    `json:"SizeRw"`
	SizeRootFs int64    `json:"SizeRootFs"`
	State      string   `json:"State"`
}

// DFVolume 是 /system/df Volumes 的項目子集；UsageData.Size == -1 表示 daemon 未統計。
type DFVolume struct {
	Name      string `json:"Name"`
	UsageData struct {
		Size     int64 `json:"Size"`
		RefCount int64 `json:"RefCount"`
	} `json:"UsageData"`
}

// DFBuildCache 是 /system/df BuildCache 的項目子集。
type DFBuildCache struct {
	ID     string `json:"ID"`
	Size   int64  `json:"Size"`
	InUse  bool   `json:"InUse"`
	Shared bool   `json:"Shared"`
}

// DiskUsage 是 /system/df 回應。
type DiskUsage struct {
	LayersSize int64          `json:"LayersSize"`
	Images     []DFImage      `json:"Images"`
	Containers []DFContainer  `json:"Containers"`
	Volumes    []DFVolume     `json:"Volumes"`
	BuildCache []DFBuildCache `json:"BuildCache"`
}

// DiskCategory 是單一類別的摘要；Unknown＝daemon 未統計大小的項目數。
type DiskCategory struct {
	Count       int   `json:"count"`
	Size        int64 `json:"size"`
	Reclaimable int64 `json:"reclaimable"`
	Unknown     int   `json:"unknown,omitempty"`
}

// DiskSummary 是四類用量與總計。
type DiskSummary struct {
	Images      DiskCategory `json:"images"`
	Containers  DiskCategory `json:"containers"`
	Volumes     DiskCategory `json:"volumes"`
	BuildCache  DiskCategory `json:"build_cache"`
	Total       int64        `json:"total"`
	Reclaimable int64        `json:"reclaimable"`
}

// DiskUsage GET /system/df（daemon 會計算 volume 大小，可能較慢 → longHC）。
func (c *Client) DiskUsage(ctx context.Context) (DiskUsage, error) {
	resp, err := c.doWith(ctx, c.longHC, http.MethodGet, "/system/df", nil, nil)
	if err != nil {
		return DiskUsage{}, err
	}
	defer resp.Body.Close()
	var v DiskUsage
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return DiskUsage{}, fmt.Errorf("decode system df: %w", err)
	}
	return v, nil
}

// Summarize 依 docker CLI（system df）的規則計算各類大小與可回收量。
func (d DiskUsage) Summarize() DiskSummary {
	var s DiskSummary

	s.Images.Count = len(d.Images)
	s.Images.Size = d.LayersSize
	var used int64
	for _, im := range d.Images {
		if im.Containers != 0 && im.Size >= 0 && im.SharedSize >= 0 {
			used += im.Size - im.SharedSize
		}
	}
	s.Images.Reclaimable = max(d.LayersSize-used, 0)

	s.Containers.Count = len(d.Containers)
	for _, ct := range d.Containers {
		s.Containers.Size += ct.SizeRw
		switch ct.State {
		case "running", "paused", "restarting":
		default:
			s.Containers.Reclaimable += ct.SizeRw
		}
	}

	s.Volumes.Count = len(d.Volumes)
	for _, v := range d.Volumes {
		if v.UsageData.Size < 0 {
			s.Volumes.Unknown++
			continue
		}
		s.Volumes.Size += v.UsageData.Size
		if v.UsageData.RefCount == 0 {
			s.Volumes.Reclaimable += v.UsageData.Size
		}
	}

	s.BuildCache.Count = len(d.BuildCache)
	for _, b := range d.BuildCache {
		if b.Shared {
			continue
		}
		s.BuildCache.Size += b.Size
		if !b.InUse {
			s.BuildCache.Reclaimable += b.Size
		}
	}

	s.Total = s.Images.Size + s.Containers.Size + s.Volumes.Size + s.BuildCache.Size
	s.Reclaimable = s.Images.Reclaimable + s.Containers.Reclaimable + s.Volumes.Reclaimable + s.BuildCache.Reclaimable
	return s
}
