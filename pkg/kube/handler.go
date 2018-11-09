package kube

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"gopkg.in/asaskevich/govalidator.v8"

	"github.com/supergiant/supergiant/pkg/clouds"
	"github.com/supergiant/supergiant/pkg/message"
	"github.com/supergiant/supergiant/pkg/model"
	"github.com/supergiant/supergiant/pkg/node"
	"github.com/supergiant/supergiant/pkg/profile"
	"github.com/supergiant/supergiant/pkg/sgerrors"
	"github.com/supergiant/supergiant/pkg/storage"
	"github.com/supergiant/supergiant/pkg/util"
	"github.com/supergiant/supergiant/pkg/workflows"
	"github.com/supergiant/supergiant/pkg/workflows/statuses"
	"github.com/supergiant/supergiant/pkg/workflows/steps"
)

type accountGetter interface {
	Get(context.Context, string) (*model.CloudAccount, error)
}

type nodeProvisioner interface {
	ProvisionNodes(context.Context, []profile.NodeProfile, *model.Kube, *steps.Config) ([]string, error)
}

type K8SServices struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		SelfLink        string `json:"selfLink"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Items []struct {
		Metadata struct {
			Name              string    `json:"name"`
			Namespace         string    `json:"namespace"`
			SelfLink          string    `json:"selfLink"`
			UID               string    `json:"uid"`
			ResourceVersion   string    `json:"resourceVersion"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
			Labels            struct {
				OperatedAlertmanager string `json:"operated-alertmanager"`
			} `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Ports []struct {
				Name       string `json:"name"`
				Protocol   string `json:"protocol"`
				Port       int    `json:"port"`
			} `json:"ports"`
			Selector struct {
				App string `json:"app"`
			} `json:"selector"`
			ClusterIP       string `json:"clusterIP"`
			Type            string `json:"type"`
			SessionAffinity string `json:"sessionAffinity"`
		} `json:"spec"`
		Status struct {
			LoadBalancer struct {
			} `json:"loadBalancer"`
		} `json:"status"`
	} `json:"items"`
}

type ServiceProxy struct{
	Name string `json:"name"`
	Type string `json:"type"`
	SelfLink string `json:"selfLink"`
}


type MetricResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Handler is a http controller for a kube entity.
type Handler struct {
	svc             Interface
	accountService  accountGetter
	nodeProvisioner nodeProvisioner
	workflowMap     map[clouds.Name]workflows.WorkflowSet
	repo            storage.Interface
	getWriter       func(string) (io.WriteCloser, error)
	getMetrics 		func(string) (*MetricResponse, error)
}

// NewHandler constructs a Handler for kubes.
func NewHandler(svc Interface, accountService accountGetter, provisioner nodeProvisioner, repo storage.Interface) *Handler {
	return &Handler{
		svc:             svc,
		accountService:  accountService,
		nodeProvisioner: provisioner,
		workflowMap: map[clouds.Name]workflows.WorkflowSet{
			clouds.DigitalOcean: {
				DeleteCluster: workflows.DigitalOceanDeleteCluster,
				DeleteNode:    workflows.DigitalOceanDeleteNode,
			},
		},
		repo:      repo,
		getWriter: util.GetWriter,
		getMetrics: func(metricURI string) (*MetricResponse, error) {
			metricResponse := &MetricResponse{}
			req, err := http.NewRequest(http.MethodGet, metricURI, nil)

			if err != nil {
				return nil, err
			}

			// TODO(stgleb): Get rid off basic auth
			req.SetBasicAuth("root", "1234")
			tr := &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			}
			client := &http.Client{
				Transport: tr,
			}
			resp, err := client.Do(req)

			if err != nil {
				return nil, err
			}

			err = json.NewDecoder(resp.Body).Decode(metricResponse)

			if err != nil {
				return nil, err
			}

			return metricResponse, nil
		},
	}
}

// Register adds kube handlers to a router.
func (h *Handler) Register(r *mux.Router) {
	r.HandleFunc("/kubes", h.createKube).Methods(http.MethodPost)
	r.HandleFunc("/kubes", h.listKubes).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}", h.getKube).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}", h.deleteKube).Methods(http.MethodDelete)

	r.HandleFunc("/kubes/{kname}/resources", h.listResources).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/resources/{resource}", h.getResource).Methods(http.MethodGet)

	r.HandleFunc("/kubes/{kname}/releases", h.installRelease).Methods(http.MethodPost)
	r.HandleFunc("/kubes/{kname}/releases", h.listReleases).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/releases/{releaseName}", h.deleteReleases).Methods(http.MethodDelete)

	r.HandleFunc("/kubes/{kname}/certs/{cname}", h.getCerts).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/tasks", h.getTasks).Methods(http.MethodGet)

	r.HandleFunc("/kubes/{kname}/nodes", h.addNode).Methods(http.MethodPost)
	r.HandleFunc("/kubes/{kname}/nodes/{nodename}", h.deleteNode).Methods(http.MethodDelete)
	r.HandleFunc("/kubes/{kname}/metrics", h.getClusterMetrics).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/nodes/metrics", h.getNodesMetrics).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/services", h.getServices).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/services/proxy", h.proxyService).Methods(http.MethodPost)
}

func (h *Handler) getTasks(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, ok := vars["kname"]

	if !ok {
		http.Error(w, "need name of a cluster", http.StatusBadRequest)
		return
	}

	tasks, err := h.getKubeTasks(r.Context(), id)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, id, err)
			return
		}

		message.SendUnknownError(w, err)
	}

	if len(tasks) == 0 {
		http.Error(w, "", http.StatusNotFound)
		return
	}

	type taskDTO struct {
		ID           string                 `json:"id"`
		Type         string                 `json:"type"`
		Status       statuses.Status        `json:"status"`
		StepStatuses []workflows.StepStatus `json:"stepsStatuses"`
	}

	resp := make([]taskDTO, 0, len(tasks))

	for _, task := range tasks {
		resp = append(resp, taskDTO{
			ID:           task.ID,
			Type:         task.Type,
			Status:       task.Status,
			StepStatuses: task.StepStatuses,
		})
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) createKube(w http.ResponseWriter, r *http.Request) {
	newKube := &model.Kube{}
	err := json.NewDecoder(r.Body).Decode(newKube)
	if err != nil {
		message.SendInvalidJSON(w, err)
		return
	}

	ok, err := govalidator.ValidateStruct(newKube)
	if !ok {
		message.SendValidationFailed(w, err)
		return
	}

	existingKube, err := h.svc.Get(r.Context(), newKube.Name)
	if existingKube != nil {
		message.SendAlreadyExists(w, existingKube.Name, sgerrors.ErrAlreadyExists)
		return
	}

	if err != nil && !sgerrors.IsNotFound(err) {
		message.SendUnknownError(w, err)
		return
	}

	if err = h.svc.Create(r.Context(), newKube); err != nil {
		message.SendUnknownError(w, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) getKube(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]

	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(k); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) listKubes(w http.ResponseWriter, r *http.Request) {
	kubes, err := h.svc.ListAll(r.Context())
	if err != nil {
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(kubes); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) deleteKube(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	acc, err := h.accountService.Get(r.Context(), k.AccountName)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}

		message.SendUnknownError(w, err)
		return
	}

	t, err := workflows.NewTask(h.workflowMap[acc.Provider].DeleteCluster, h.repo)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}

		message.SendUnknownError(w, err)
		return
	}

	config := &steps.Config{
		ClusterName:      k.Name,
		CloudAccountName: k.AccountName,
	}

	err = util.FillCloudAccountCredentials(r.Context(), acc, config)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	writer, err := h.getWriter(t.ID)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}

	errChan := t.Run(context.Background(), *config, writer)

	go func(t *workflows.Task) {
		// Update kube with deleting state
		k.State = model.StateDeleting
		err = h.svc.Create(context.Background(), k)

		if err != nil {
			logrus.Errorf("update cluster %s caused %v", kname, err)
		}

		err = <-errChan
		if err != nil {
			return
		}

		// Finally delete cluster record from etcd
		if err := h.svc.Delete(context.Background(), kname); err != nil {
			logrus.Errorf("delete kube %s caused %v", kname, err)
			return
		}

		h.deleteClusterTasks(context.Background(), kname)
	}(t)

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) listResources(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	rawResources, err := h.svc.ListKubeResources(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if _, err = w.Write(rawResources); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) getResource(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	rs := vars["resource"]
	ns := r.URL.Query().Get("namespace")
	name := r.URL.Query().Get("name")

	rawResources, err := h.svc.GetKubeResources(r.Context(), kname, rs, ns, name)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if _, err = w.Write(rawResources); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) getCerts(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	cname := vars["cname"]

	b, err := h.svc.GetCerts(r.Context(), kname, cname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(b); err != nil {
		message.SendUnknownError(w, err)
	}
}

// Add node to working kube
func (h *Handler) addNode(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	kname := vars["kname"]
	k, err := h.svc.Get(r.Context(), kname)

	// TODO(stgleb): This method contains a lot of specific stuff, implement provision node
	// method for nodeProvisioner to do all things related to provisioning and saving cluster state
	if sgerrors.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	nodeProfiles := make([]profile.NodeProfile, 0)
	err = json.NewDecoder(r.Body).Decode(&nodeProfiles)

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	acc, err := h.accountService.Get(r.Context(), k.AccountName)

	if sgerrors.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	kubeProfile := profile.Profile{
		Provider:        acc.Provider,
		Region:          k.Region,
		Arch:            k.Arch,
		OperatingSystem: k.OperatingSystem,
		UbuntuVersion:   k.OperatingSystemVersion,
		DockerVersion:   k.DockerVersion,
		K8SVersion:      k.K8SVersion,
		HelmVersion:     k.HelmVersion,

		NetworkType:    k.Networking.Type,
		CIDR:           k.Networking.CIDR,
		FlannelVersion: k.Networking.Version,

		NodesProfiles: []profile.NodeProfile{
			{},
		},

		RBACEnabled: k.RBACEnabled,
	}

	config := steps.NewConfig(k.Name, "", k.AccountName, kubeProfile)
	config.CertificatesConfig.CAKey = k.Auth.CAKey
	config.CertificatesConfig.CACert = k.Auth.CACert

	if len(k.Masters) != 0 {
		config.AddMaster(util.GetRandomNode(k.Masters))
	} else {
		http.Error(w, "no master found", http.StatusNotFound)
		return
	}

	// Get cloud account fill appropriate config structure with cloud account credentials
	err = util.FillCloudAccountCredentials(r.Context(), acc, config)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, _ := context.WithTimeout(context.Background(), time.Minute*10)
	tasks, err := h.nodeProvisioner.ProvisionNodes(ctx, nodeProfiles, k, config)

	if err != nil && sgerrors.IsNotFound(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Respond to client side that request has been accepted
	w.WriteHeader(http.StatusAccepted)
	err = json.NewEncoder(w).Encode(tasks)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		logrus.Error(errors.Wrap(err, "marshal json"))
	}
}

// TODO(stgleb): cover with unit tests
func (h *Handler) deleteNode(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	nodeName := vars["nodename"]

	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	// TODO(stgleb): check whether we will have quorum of master nodes if node is deleted.
	if _, ok := k.Masters[nodeName]; ok {
		http.Error(w, "delete master node not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, ok := k.Nodes[nodeName]; !ok {
		http.NotFound(w, r)
		return
	}

	acc, err := h.accountService.Get(r.Context(), k.AccountName)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}

		message.SendUnknownError(w, err)
		return

	}

	t, err := workflows.NewTask(h.workflowMap[acc.Provider].DeleteNode, h.repo)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}

		message.SendUnknownError(w, err)
		return
	}

	config := &steps.Config{
		ClusterName:      k.Name,
		CloudAccountName: k.AccountName,
		Node: node.Node{
			Name: nodeName,
		},
	}

	err = util.FillCloudAccountCredentials(r.Context(), acc, config)

	if err != nil {
		if sgerrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	writer, err := h.getWriter(t.ID)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}

	errChan := t.Run(context.Background(), *config, writer)

	// Update cluster state when deletion completes
	go func() {
		// Set node to deleting state
		nodeToDelete, ok := k.Nodes[nodeName]

		if !ok {
			logrus.Errorf("Node %s not found", nodeName)
			return
		}
		nodeToDelete.State = node.StateDeleting
		k.Nodes[nodeName] = nodeToDelete
		err := h.svc.Create(context.Background(), k)

		if err != nil {
			logrus.Errorf("update cluster %s caused %v", kname, err)
		}

		err = <-errChan

		if err != nil {
			logrus.Errorf("delete node %s from cluster %s caused %v", nodeName, kname, err)
		}

		// Delete node from cluster object
		delete(k.Nodes, nodeName)
		// Save cluster object to etcd
		logrus.Infof("delete node %s from cluster %s", nodeName, kname)
		err = h.svc.Create(context.Background(), k)

		if err != nil {
			logrus.Errorf("update cluster %s caused %v", kname, err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// TODO(stgleb): Create separte task service to manage task object lifecycle
func (h *Handler) getKubeTasks(ctx context.Context, kubeName string) ([]*workflows.Task, error) {
	data, err := h.repo.GetAll(ctx, workflows.Prefix)

	if err != nil {
		return nil, errors.Wrap(err, "get cluster tasks")
	}

	tasks := make([]*workflows.Task, 0)
	for _, v := range data {
		task := &workflows.Task{}
		err := json.Unmarshal(v, task)
		if err != nil {
			return nil, errors.Wrap(err, "unmarshal task data")
		}

		if task != nil && task.Config != nil && task.Config.ClusterName == kubeName {
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

func (h *Handler) deleteClusterTasks(ctx context.Context, clusterName string) error {
	tasks, err := h.getKubeTasks(ctx, clusterName)

	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("delete cluster %s tasks", clusterName))
	}

	for _, task := range tasks {
		if err := h.repo.Delete(ctx, workflows.Prefix, task.ID); err != nil {
			logrus.Warnf("delete task %s: %v", task.ID, err)
			return err
		}
	}

	return nil
}

func (h *Handler) installRelease(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	inp := &ReleaseInput{}
	err := json.NewDecoder(r.Body).Decode(inp)
	if err != nil {
		logrus.Errorf("helm: install release: decode: %s", err)
		message.SendInvalidJSON(w, err)
		return
	}
	ok, err := govalidator.ValidateStruct(inp)
	if !ok {
		logrus.Errorf("helm: install release: validation: %s", err)
		message.SendValidationFailed(w, err)
		return
	}

	kname := vars["kname"]
	rls, err := h.svc.InstallRelease(r.Context(), kname, inp)
	if err != nil {
		logrus.Errorf("helm: install release: %s cluster: %s (%+v)", kname, err, inp)
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(rls); err != nil {
		logrus.Errorf("helm: install release: %s cluster: %s/%s: write response: %s",
			kname, inp.RepoName, inp.ChartName, err)
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) listReleases(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	// TODO: use a struct for input parameters
	rlsList, err := h.svc.ListReleases(r.Context(), kname, "", "", 0)
	if err != nil {
		logrus.Errorf("helm: list releases: %s cluster: %s", kname, err)
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(rlsList); err != nil {
		logrus.Errorf("helm: list releases: %s cluster: write response: %s", kname, err)
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) deleteReleases(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	rlsName := vars["releaseName"]
	purge, _ := strconv.ParseBool(r.URL.Query().Get("purge"))

	rls, err := h.svc.DeleteRelease(r.Context(), kname, rlsName, purge)
	if err != nil {
		logrus.Errorf("helm: delete release: cluster %s: release %s: %s", kname, rlsName, err)
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(rls); err != nil {
		logrus.Errorf("helm: delete release: %s cluster: write response: %s", kname, err)
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) getClusterMetrics(w http.ResponseWriter, r *http.Request) {
	var (
		metricsRelUrls = map[string]string{
			"cpu":    "api/v1/query?query=:node_cpu_utilisation:avg1m",
			"memory": "api/v1/query?query=:node_memory_utilisation:",
		}
		masterNode     *node.Node
		response       = map[string]interface{}{}
		baseUrl        = "api/v1/namespaces/default/services/prometheus-operated:9090/proxy"
	)

	vars := mux.Vars(r)
	kname := vars["kname"]

	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	for key := range k.Masters {
		if k.Masters[key] != nil {
			masterNode = k.Masters[key]
		}
	}

	for metricType, relUrl := range metricsRelUrls {
		url := fmt.Sprintf("https://%s/%s/%s", masterNode.PublicIp, baseUrl, relUrl)
		metricResponse, err := h.getMetrics(url)

		if err != nil {
			message.SendUnknownError(w, err)
			return
		}

		if len(metricResponse.Data.Result) > 0 && len(metricResponse.Data.Result[0].Value) > 1 {
			response[metricType] = metricResponse.Data.Result[0].Value[1]
		}
	}

	err = json.NewEncoder(w).Encode(response)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}
}

func (h *Handler) getNodesMetrics(w http.ResponseWriter, r *http.Request) {
	var (
		metricsRelUrls = map[string]string{
			"cpu":    "api/v1/query?query=node:node_cpu_utilisation:avg1m",
			"memory": "api/v1/query?query=node:node_memory_utilisation:",
		}
		masterNode     *node.Node
		response       = map[string]map[string]interface{}{}
		baseUrl        = "api/v1/namespaces/default/services/prometheus-operated:9090/proxy"
	)

	vars := mux.Vars(r)
	kname := vars["kname"]

	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	for key := range k.Masters {
		if k.Masters[key] != nil {
			masterNode = k.Masters[key]
		}
	}

	for metricType, relUrl := range metricsRelUrls {
		url := fmt.Sprintf("https://%s/%s/%s", masterNode.PublicIp, baseUrl, relUrl)
		metricResponse, err := h.getMetrics(url)

		if err != nil {
			message.SendUnknownError(w, err)
			return
		}

		for _, result := range metricResponse.Data.Result {
			// Get node name of the metric
			nodeName, ok := result.Metric["node"]

			if !ok {
				continue
			}
			// If dict for this node is empty - fill it with empty map
			if response[nodeName] == nil {
				response[nodeName] = map[string]interface{}{}
			}

			response[nodeName][metricType] = result.Value[1]
		}
	}

	err = json.NewEncoder(w).Encode(response)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}
}

func (h *Handler) getServices(w http.ResponseWriter, r *http.Request) {
	var (
		servicesUrl = "api/v1/services"
		kname string
		masterNode *node.Node
		k8sServices  = &K8SServices{}
	)
	vars := mux.Vars(r)
	kname = vars["kname"]

	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	for key := range k.Masters {
		if k.Masters[key] != nil {
			masterNode = k.Masters[key]
		}
	}

	url := fmt.Sprintf("https://%s/%s", masterNode.PublicIp, servicesUrl)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	req.SetBasicAuth("root", "1234")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
	}
	resp, err := client.Do(req)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}

	err = json.NewDecoder(resp.Body).Decode(k8sServices)

	if err != nil {
		logrus.Error(err)
		message.SendInvalidJSON(w, err)
		return
	}

	webPorts := map[string]struct{}{
		"web": {},
		"http": {},
		"https": {},
		"service": {},
	}

	 services := make([]ServiceProxy, 0)

	for _, service := range k8sServices.Items {
		for _, port := range service.Spec.Ports {
			if port.Protocol == "TCP" {
				if _, ok := webPorts[port.Name]; ok {
					service := ServiceProxy{
						Name: service.Metadata.Name,
						Type: service.Spec.Type,
						SelfLink: fmt.Sprintf("https://%s%s:%d/proxy",
							masterNode.PublicIp, service.Metadata.SelfLink, port.Port),
					}
					services = append(services, service)
				}
			}
		}
	}

	err = json.NewEncoder(w).Encode(services)

	if err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) proxyService(w http.ResponseWriter, r *http.Request) {
	serviceProxy := &ServiceProxy{}
	err := json.NewDecoder(r.Body).Decode(serviceProxy)

	req, err := http.NewRequest(http.MethodGet, serviceProxy.SelfLink, nil)
	req.SetBasicAuth("root", "1234")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
	}
	resp, err := client.Do(req)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}

	_, err = io.Copy(w, resp.Body)

	if err != nil {
		message.SendUnknownError(w, err)
		return
	}
}