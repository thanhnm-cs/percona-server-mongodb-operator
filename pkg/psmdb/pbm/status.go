package pbm

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/percona/percona-server-mongodb-operator/clientcmd"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
)

type Snapshot struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Status     string `json:"status"`
	RestoreTo  int64  `json:"restoreTo"`
	PBMVersion string `json:"pbmVersion"`
	Type       string `json:"type"`
	Source     string `json:"src"`
}

type PITRChunk struct {
	Range struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"range"`
}

type Backups struct {
	Type       string     `json:"type"`
	Path       string     `json:"path"`
	Region     string     `json:"region"`
	Snapshots  []Snapshot `json:"snapshot"`
	PITRChunks struct {
		Size   int64       `json:"size"`
		Chunks []PITRChunk `json:"pitrChunks"`
	} `json:"pitrChunks"`
}

type Node struct {
	Host  string `json:"host"`
	Agent string `json:"agent"`
	Role  string `json:"role"`
	OK    bool   `json:"ok"`
}

type Cluster struct {
	ReplSet string `json:"rs"`
	Nodes   []Node `json:"nodes"`
}

type Running struct {
	OpID    string `json:"opId"`
	Status  string `json:"status"`
	StartTS int64  `json:"startTS"`
	Name    string `json:"name"`
	Type    string `json:"type"`
}

type Status struct {
	Backups Backups   `json:"backups"`
	Cluster []Cluster `json:"cluster"`
	PITR    struct {
		Conf  bool   `json:"conf"`
		Run   bool   `json:"run"`
		Error string `json:"error"`
	} `json:"pitr"`
	Running Running `json:"running"`
}

// GetStatus returns the status of PBM
func GetStatus(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) (Status, error) {
	status := Status{}

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"pbm", "status", "-o", "json"}

	err := exec(ctx, cli, pod, BackupAgentContainerName, cmd, nil, &stdout, &stderr)
	if err != nil {
		return status, errors.Wrapf(err, "stdout: %s stderr: %s", stdout.String(), stderr.String())
	}

	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		return status, err
	}

	return status, nil
}

func GetRunningOperation(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) (Running, error) {
	running := Running{}

	status, err := GetStatus(ctx, cli, pod)
	if err != nil {
		return running, err
	}

	return status.Running, nil
}

// HasRunningOperation checks if there is a running operation in PBM
func HasRunningOperation(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) (bool, error) {
	status, err := GetStatus(ctx, cli, pod)
	if err != nil {
		if IsNotConfigured(err) {
			return false, nil
		}
		return false, err
	}

	return status.Running.Status != "", nil
}

// IsPITRRunning checks if PITR is running or enabled in config
func IsPITRRunning(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) (bool, error) {
	status, err := GetStatus(ctx, cli, pod)
	if err != nil {
		return false, err
	}

	return status.PITR.Run || status.PITR.Conf, nil
}

func LatestPITRChunk(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) (string, error) {
	status, err := GetStatus(ctx, cli, pod)
	if err != nil {
		return "", err
	}

	latest := status.Backups.PITRChunks.Chunks[len(status.Backups.PITRChunks.Chunks)-1].Range.End
	ts := time.Unix(int64(latest), 0).UTC()

	return ts.Format("2006-01-02T15:04:05"), nil
}