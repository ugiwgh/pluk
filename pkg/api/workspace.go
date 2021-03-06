package api

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/emicklei/go-restful"
	"github.com/kuberlab/lib/pkg/dealerclient"
	"github.com/kuberlab/lib/pkg/errors"
	"github.com/kuberlab/pluk/pkg/plukclient"
	"github.com/kuberlab/pluk/pkg/types"
	"github.com/kuberlab/pluk/pkg/utils"
)

func (api *API) checkWorkspace(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")

	u := utils.AuthValidationURL()
	if u == "" && !utils.HasMasters() {
		resp.WriteEntity(&types.Workspace{Name: workspace})
		return
	}

	if u == "" && utils.HasMasters() {
		// Request master.
		masters := plukclient.NewMasterClientFromHeaders(req.Request.Header)
		ws, err := masters.CheckWorkspace(workspace)
		if err != nil {
			WriteError(resp, err)
			return
		}
		resp.WriteEntity(ws)
		return
	}

	dealer, err := api.dealerClient(req)
	if err != nil {
		WriteError(resp, err)
		return
	}
	ws, err := dealer.GetWorkspace(workspace)
	if err != nil {
		WriteError(resp, err)
		return
	}
	resp.WriteEntity(ws)
}

func (api *API) checkEntityAccess(req *restful.Request, write bool) (*types.Dataset, error) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("dataset")

	if name == "" {
		name = req.PathParameter("name")
	}

	u := utils.AuthValidationURL()
	if u == "" && !utils.HasMasters() {
		return &types.Dataset{
			Name:      name,
			Workspace: workspace,
			DType:     currentType(req),
		}, nil
	}

	if u == "" && utils.HasMasters() {
		// Request master.
		masters := plukclient.NewMasterClientFromHeaders(req.Request.Header)
		ds, err := masters.CheckEntityPermission(currentType(req), workspace, name, write)
		if err != nil {
			return nil, err
		}
		return ds, nil
	}

	dealer, err := api.dealerClient(req)
	if err != nil {
		return nil, err
	}

	ws, err := dealer.GetWorkspace(workspace)
	if err != nil {
		return nil, err
	}

	var entityName = currentType(req)
	if currentType(req) == "model" {
		entityName = "mlmodel"
	}

	var modificator string
	if write {
		modificator = "manage"
	} else {
		modificator = "read"
	}
	neededPerm := fmt.Sprintf("%v.%v", entityName, modificator)

	if strings.Contains(strings.Join(ws.Can, " "), neededPerm) {
		// Found needed permission
		return &types.Dataset{Name: name, Workspace: workspace, DType: currentType(req)}, nil
	} else {
		// If read, then check if item exists.
		if !write {
			switch currentType(req) {
			case "model":
				_, err = dealer.GetModel(workspace, name)
			case "dataset":
				_, err = dealer.GetDataset(workspace, name)
			}
			if err != nil {
				return nil, err
			} else {
				return &types.Dataset{Name: name, Workspace: workspace, DType: currentType(req)}, nil
			}
		}
		return nil, errors.NewStatus(
			http.StatusForbidden,
			fmt.Sprintf("Failed to %v %v", modificator, currentType(req)),
		)
	}
}

func (api *API) checkDatasetExists(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("dataset")
	// Check can write
	err := api.checkEntityExists(req, workspace, name)
	if err != nil {
		WriteError(resp, err)
		return
	}
	ds := &types.Dataset{
		Name:      name,
		Workspace: workspace,
		DType:     currentType(req),
	}
	resp.WriteEntity(ds)
}

func (api *API) checkDatasetPermission(req *restful.Request, resp *restful.Response) {
	writeRaw := req.QueryParameter("write")
	write, _ := strconv.ParseBool(writeRaw)

	// Check can write
	ds, err := api.checkEntityAccess(req, write)
	if err != nil {
		WriteError(resp, err)
		return
	}

	resp.WriteEntity(ds)
}

func (api *API) checkEntityExists(req *restful.Request, ws, name string) error {
	u := utils.AuthValidationURL()
	if u == "" && !utils.HasMasters() {
		return nil
	}

	if u == "" && utils.HasMasters() {
		// Request master.
		masters := plukclient.NewMasterClientFromHeaders(req.Request.Header)
		_, err := masters.CheckEntityExists(currentType(req), ws, name)
		return err
	}

	dealer, err := api.dealerClient(req)
	if err != nil {
		return err
	}

	var getMethod func(string, string) (*dealerclient.Dataset, error)
	switch currentType(req) {
	case "dataset":
		getMethod = dealer.GetDataset
	case "model":
		getMethod = dealer.GetModel
	}

	_, err = getMethod(ws, name)
	return err
}

func (api *API) postSpecToDealer(req *restful.Request, ws, name, version string, spec interface{}) error {
	u := utils.AuthValidationURL()
	if u == "" && !utils.HasMasters() {
		return nil
	}

	if u == "" && utils.HasMasters() {
		// Request master.
		masters := plukclient.NewMasterClientFromHeaders(req.Request.Header)
		var err error
		if version != "" {
			err = masters.PostEntitySpecForVersion(currentType(req), ws, name, version, spec)
		} else {
			err = masters.PostEntitySpec(currentType(req), ws, name, spec)
		}
		return err
	}

	dealer, err := api.dealerClient(req)
	if err != nil {
		return err
	}

	switch currentType(req) {
	case "dataset":
		return nil
	case "model":
		if version != "" {
			err = dealer.CreateSpecForVersion(ws, name, version, spec)
		} else {
			err = dealer.CreateSpec(ws, name, spec)
		}
		return err
	}

	return nil
}

func (api *API) postSpec(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("dataset")

	var spec io.Reader
	specRaw, err := ioutil.ReadAll(req.Request.Body)
	req.Request.Body.Close()
	if err != nil {
		spec = nil
	} else if len(specRaw) > 1 {
		spec = bytes.NewBuffer(specRaw)
	}

	err = api.postSpecToDealer(req, workspace, name, "", spec)
	if err != nil {
		WriteError(resp, err)
		return
	}

	resp.WriteHeader(http.StatusCreated)
	resp.Write([]byte("Ok!\n"))
}

func (api *API) postVersionSpec(req *restful.Request, resp *restful.Response) {
	workspace := req.PathParameter("workspace")
	name := req.PathParameter("dataset")
	version := req.PathParameter("version")

	var spec io.Reader
	specRaw, err := ioutil.ReadAll(req.Request.Body)
	req.Request.Body.Close()
	if err != nil {
		spec = nil
	} else if len(specRaw) > 1 {
		spec = bytes.NewBuffer(specRaw)
	}

	err = api.postSpecToDealer(req, workspace, name, version, spec)
	if err != nil {
		WriteError(resp, err)
		return
	}

	resp.WriteHeader(http.StatusCreated)
	resp.Write([]byte("Ok!\n"))
}
