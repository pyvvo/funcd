package envelope

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// LoadMaster migrates a working-directory master and loads the node master secret from its place, gateway on or off
// (Decision 7). The daemon and funcd upgrade (ADR-0207) load it alike.
func LoadMaster(cfg config.Config, log *slog.Logger) ([]byte, error) {
	file, dataDir := cfg.S3Gateway.MasterSecretFile, cfg.Storage.DataDir
	if err := s3gateway.MigrateMaster(file, dataDir, cfg.S3Gateway.Enabled, log); err != nil {
		return nil, err
	}
	return s3gateway.LoadOrCreateMaster(file, dataDir)
}

// ReadSecretsKey reads secrets.encryptionKeyFile; "" ⇒ nil.
func ReadSecretsKey(cfg config.Config) ([]byte, error) {
	if cfg.Secrets.EncryptionKeyFile == "" {
		return nil, nil
	}
	key, err := os.ReadFile(cfg.Secrets.EncryptionKeyFile) //nolint:gosec // operator-supplied key path
	if err != nil {
		return nil, fmt.Errorf("read secrets.encryptionKeyFile %s: %w", cfg.Secrets.EncryptionKeyFile, err)
	}
	return key, nil
}

// FromConfig applies Decision 2's start rules when backup.target is set: a violation is refused, and the keys'
// fingerprints are logged. nil without a target.
func FromConfig(cfg config.Config, master []byte, log *slog.Logger) (*Sealer, error) {
	if cfg.Backup.Target == "" {
		return nil, nil
	}
	key, err := ReadSecretsKey(cfg)
	if err != nil {
		return nil, err
	}
	return New(Config{
		Recipients: cfg.Backup.Encryption.Recipients,
		None:       cfg.Backup.Encryption.None,
		SecretsKey: key,
		Master:     master,
		Logger:     log,
	})
}
