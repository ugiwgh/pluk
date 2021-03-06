package api

import (
	"fmt"
	"net/http"

	"github.com/emicklei/go-restful"
	"github.com/kuberlab/lib/pkg/dealerclient"
	"github.com/kuberlab/pluk/pkg/datasets"
	"github.com/kuberlab/pluk/pkg/db"
	"github.com/kuberlab/pluk/pkg/gc"
	"github.com/kuberlab/pluk/pkg/types"
	"github.com/kuberlab/pluk/pkg/utils"
)

func (api *API) versions(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("name")
	master := api.masterClient(req)

	err := api.checkEntityExists(req, workspace, name)
	if err != nil {
		WriteError(resp, err)
		return
	}

	dataset, err := api.ds.GetDataset(currentType(req), workspace, name, master)
	if err != nil {
		WriteError(resp, EntityNotFoundError(req, name, err))
		return
	}
	versions, err := dataset.Versions()
	if err != nil {
		WriteStatusError(resp, http.StatusInternalServerError, err)
		return
	}

	// Cache last 3 versions.
	//onlyVersions := make([]string, 0)
	//for _, v := range versions {
	//	onlyVersions = append(onlyVersions, v.Version)
	//}
	//go api.cacheFS(dataset, utils.GetFirstN(onlyVersions, 3))
	resp.WriteEntity(types.VersionList{Versions: versions})
}

func (api *API) getVersion(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("name")
	version := req.PathParameter("version")
	master := api.masterClient(req)

	dataset, err := api.ds.GetDataset(currentType(req), workspace, name, master)
	if err != nil {
		WriteError(resp, EntityNotFoundError(req, name, err))
		return
	}

	ver, err := api.findDatasetVersion(dataset, version, true)
	if err != nil {
		WriteError(resp, err)
		return
	}

	resp.WriteEntity(ver)
}

func (api *API) createVersion(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("name")
	version := req.PathParameter("version")
	message := req.QueryParameter("message")
	master := api.masterClient(req)

	// Wait
	acquireConcurrency()
	defer releaseConcurrency()
	gc.WaitGCCompleted()

	dataset, _ := api.ds.GetDataset(currentType(req), workspace, name, master)
	if dataset == nil {
		// Create
		var err error
		dataset, err = api.ds.NewDataset(currentType(req), workspace, name, master)
		if err != nil {
			WriteError(resp, err)
			return
		}
	}

	if err := utils.CheckVersion(version); err != nil {
		WriteStatusError(resp, http.StatusBadRequest, err)
		return
	}

	versions, err := dataset.Versions()
	if err != nil {
		WriteStatusError(resp, http.StatusInternalServerError, err)
		return
	}

	for _, v := range versions {
		if v.Version == version {
			WriteStatusError(
				resp,
				http.StatusConflict,
				fmt.Errorf("Version %v for %v %v/%v already exists", currentType(req), version, workspace, name),
			)
		}
	}

	dsv := &db.DatasetVersion{
		Version:   version,
		Name:      name,
		Workspace: workspace,
		Editing:   true,
		Message:   message,
		Type:      currentType(req),
	}

	if err := datasets.SaveDatasetVersion(api.mgr, dsv); err != nil {
		WriteError(resp, err)
		return
	}

	res := types.Version{
		Version:   version,
		DType:     dsv.Type,
		CreatedAt: dsv.CreatedAt,
		UpdatedAt: dsv.UpdatedAt,
		Message:   dsv.Message,
		Editing:   dsv.Editing,
		SizeBytes: dsv.Size,
		Workspace: dsv.Workspace,
		Name:      dsv.Name,
		FileCount: dsv.FileCount,
	}

	resp.WriteHeaderAndEntity(http.StatusCreated, res)
}

func (api *API) deleteVersion(req *restful.Request, resp *restful.Response) {
	name := req.PathParameter("name")
	version := req.PathParameter("version")
	workspace := req.PathParameter("workspace")
	master := api.masterClient(req)

	acquireConcurrency()
	defer releaseConcurrency()

	dataset, err := api.ds.GetDataset(currentType(req), workspace, name, master)
	if err != nil {
		WriteError(resp, EntityNotFoundError(req, name, err))
		return
	}

	err = dataset.DeleteVersion(version, true)
	if err != nil {
		WriteStatusError(resp, http.StatusInternalServerError, err)
		return
	}

	api.ds.PushMessageVersion(
		&types.Version{Workspace: workspace, Name: name, DType: currentType(req), Version: version},
	)

	// Invalidate cache
	api.invalidateVersionCache(dataset, version)
	resp.WriteHeader(http.StatusNoContent)
}

func (api *API) cloneVersion(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("name")
	version := req.PathParameter("version")
	targetVersion := req.PathParameter("targetVersion")
	message := req.QueryParameter("message")
	master := api.masterClient(req)

	acquireConcurrency()
	defer releaseConcurrency()

	dataset, err := api.ds.GetDataset(currentType(req), workspace, name, master)
	if err != nil {
		WriteError(resp, EntityNotFoundError(req, name, err))
		return
	}

	if err := utils.CheckVersion(targetVersion); err != nil {
		WriteStatusError(resp, http.StatusBadRequest, err)
		return
	}
	api.invalidateVersionCache(dataset, targetVersion)

	dsv, err := dataset.CloneVersion(version, targetVersion, message)
	if err != nil {
		WriteStatusError(resp, http.StatusInternalServerError, err)
		return
	}

	api.ds.PushMessageVersion(
		&types.Version{Workspace: workspace, Name: name, DType: currentType(req), Version: version},
	)

	resp.WriteHeaderAndEntity(http.StatusCreated, dsv)
}

func (api *API) commitVersion(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("name")
	version := req.PathParameter("version")
	message := req.QueryParameter("message")
	master := api.masterClient(req)

	acquireConcurrency()
	defer releaseConcurrency()

	dataset, err := api.ds.GetDataset(currentType(req), workspace, name, master)
	if err != nil {
		WriteError(resp, EntityNotFoundError(req, name, err))
		return
	}

	dsv, err := dataset.CommitVersion(version, message)
	if err != nil {
		WriteError(resp, err)
		return
	}
	vers, err := dataset.Versions()
	if err != nil {
		WriteError(resp, err)
		return
	}
	api.reportNewVersion(
		req,
		dealerclient.NewVersion{
			Workspace: workspace,
			Version:   version,
			Type:      currentType(req),
			Name:      name,
			Latest:    len(vers) > 0 && vers[0].Version == version,
		},
	)

	resp.WriteEntity(dsv)
}
