package pbm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-backup-mongodb/pbm/config"
	"github.com/percona/percona-backup-mongodb/pbm/storage"
	"github.com/percona/percona-server-mongodb-operator/clientcmd"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
)

const ConfigFilePath = "/etc/pbm/config.yaml"

func IsNotConfigured(err error) bool {
	return strings.Contains(err.Error(), "mongo: no documents in result")
}

// FileExists checks if a file exists in the PBM container
func FileExists(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod, path string) bool {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"test", "-f", path}

	err := exec(ctx, cli, pod, cmd, &stdout, &stderr)

	return err == nil
}

// SetConfigFile sets the PBM configuration file
func SetConfigFile(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod, path string) error {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"pbm", "config", "--file", path}

	err := exec(ctx, cli, pod, cmd, &stdout, &stderr)
	if err != nil {
		return errors.Wrap(err, stderr.String())
	}

	return nil
}

// SetConfigKey sets the PBM configuration key
func SetConfigVar(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod, key, value string) error {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"pbm", "config", fmt.Sprintf("--set=%s=%s", key, value)}

	err := exec(ctx, cli, pod, cmd, &stdout, &stderr)
	if err != nil {
		return errors.Wrapf(err, stderr.String())
	}

	return nil
}

// ForceResync forces a resync of the PBM storage
func ForceResync(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) error {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"pbm", "config", "--force-resync"}

	err := exec(ctx, cli, pod, cmd, &stdout, &stderr)
	if err != nil {
		return errors.Wrap(err, stderr.String())
	}

	return nil
}

// CheckSHA256Sum checks the SHA256 checksum of a file in the PBM container
func CheckSHA256Sum(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod, checksum, path string) bool {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"bash", "-c", fmt.Sprintf("echo %s %s | sha256sum --check --status", checksum, path)}

	err := exec(ctx, cli, pod, cmd, &stdout, &stderr)

	return err == nil
}

// GetConfigChecksum returns the SHA256 checksum of the *applied* PBM configuration
func GetConfigChecksum(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) (string, error) {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := []string{"pbm", "config"}

	err := exec(ctx, cli, pod, cmd, &stdout, &stderr)
	if err != nil {
		return "", errors.Wrap(err, stderr.String())
	}

	sha256sum := fmt.Sprintf("%x", sha256.Sum256(stdout.Bytes()))

	return sha256sum, nil
}

// GenerateConfig generates a PBM configuration based on the PerconaServerMongoDB CR
func GenerateConfig(ctx context.Context, k8sclient client.Client, cr *psmdbv1.PerconaServerMongoDB, stg psmdbv1.BackupStorageSpec) (config.Config, error) {
	cnf := config.Config{
		PITR: config.PITRConf{
			Enabled:          cr.Spec.Backup.PITR.Enabled,
			OplogSpanMin:     cr.Spec.Backup.PITR.OplogSpanMin.Float64(),
			OplogOnly:        cr.Spec.Backup.PITR.OplogOnly,
			Compression:      cr.Spec.Backup.PITR.CompressionType,
			CompressionLevel: cr.Spec.Backup.PITR.CompressionLevel,
		},
	}

	switch stg.Type {
	case storage.S3:
		creds, err := GetS3Crendentials(ctx, k8sclient, cr.Namespace, stg.S3)
		if err != nil {
			return cnf, errors.Wrap(err, "get S3 credentials")
		}
		cnf.Storage = config.StorageConf{
			Type: storage.S3,
			S3:   NewS3Config(stg.S3, creds),
		}
	case storage.Azure:
		account, creds, err := GetAzureCrendentials(ctx, k8sclient, cr.Namespace, stg.Azure)
		if err != nil {
			return cnf, errors.Wrap(err, "get Azure credentials")
		}
		cnf.Storage = config.StorageConf{
			Type:  storage.Azure,
			Azure: NewAzureConfig(stg.Azure, account, creds),
		}
	}

	return cnf, nil
}

func CreateOrUpdateConfig(ctx context.Context, cli *clientcmd.Client, k8sclient client.Client, cr *psmdbv1.PerconaServerMongoDB, stg psmdbv1.BackupStorageSpec) error {
	l := log.FromContext(ctx)

	cnf, err := GenerateConfig(ctx, k8sclient, cr, stg)
	if err != nil {
		return errors.Wrap(err, "get config")
	}

	cnfBytes, err := yaml.Marshal(cnf)
	if err != nil {
		return errors.Wrap(err, "marshal config")
	}

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pbm-config",
			Namespace: cr.Namespace,
		},
	}

	sha256sum := fmt.Sprintf("%x", sha256.Sum256(cnfBytes))

	err = k8sclient.Get(ctx, client.ObjectKeyFromObject(&secret), &secret)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			l.Info("Creating PBM config secret", "secret", secret.Name, "checksum", sha256sum)
			secret.Annotations = make(map[string]string)
			secret.Annotations["percona.com/config-sum"] = sha256sum
			secret.Data = make(map[string][]byte)
			secret.Data["config.yaml"] = cnfBytes
			err = k8sclient.Create(ctx, &secret)
			if err != nil {
				return errors.Wrap(err, "create secret")
			}
			return nil
		}

		return errors.Wrap(err, "get secret")
	}

	if reflect.DeepEqual(secret.Data["config.yaml"], cnfBytes) {
		l.V(1).Info("PBM config secret is up to date", "secret", secret.Name)
		return nil
	}

	l.Info("Updating PBM config secret", "secret", secret.Name, "checksum", sha256sum)

	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	delete(secret.Annotations, "percona.com/config-applied")
	secret.Annotations["percona.com/config-sum"] = sha256sum

	secret.Data["config.yaml"] = cnfBytes
	err = k8sclient.Update(ctx, &secret)
	if err != nil {
		return errors.Wrap(err, "update secret")
	}

	return nil
}

func EnablePITR(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) error {
	return SetConfigVar(ctx, cli, pod, "pitr.enabled", "true")
}

func DisablePITR(ctx context.Context, cli *clientcmd.Client, pod *corev1.Pod) error {
	return SetConfigVar(ctx, cli, pod, "pitr.enabled", "false")
}
