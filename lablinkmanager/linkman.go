package lablinkmanager

import (
	"fmt"
	"os"
	"path/filepath"
)

const DISKWIPE_LABNAME = "diskwipe"

type LinkManager struct {
	active string
	folder string
	link   string
}

func NewLinkManager(folder string, link string, active string) *LinkManager {
	return &LinkManager{
		folder: folder,
		link:   link,
		active: active,
	}
}

func (m *LinkManager) EnableLab() error {
	return m.internalSet(m.active)
}

func (m *LinkManager) EnableDiskWipe() error {
	return m.internalSet(DISKWIPE_LABNAME)
}

func (m *LinkManager) internalSet(activate string) error {
	os.Remove(filepath.Join(m.folder, m.link))
	err := os.Symlink(filepath.Join(m.folder, activate), filepath.Join(m.folder, m.link))
	if err != nil {
		return fmt.Errorf("failed to enable %s lab: %w", activate, err)
	}
	return nil
}
