// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package uki creates the UKI file out of the sd-stub and other sections.
package uki

import (
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/itxaka/go-secureboot/pkg/measure"
	"github.com/itxaka/go-secureboot/pkg/pesign"
	"github.com/itxaka/go-secureboot/pkg/secureboot"
)

// section is a UKI file section.
type section struct {
	// Section name.
	Name secureboot.Section
	// Path to the contents of the section.
	Path string
	// Should the section be measured to the TPM?
	Measure bool
	// Should the section be appended, or is it already in the PE file.
	Append bool
	// Size & VMA of the section.
	Size uint64
	VMA  uint64
}

// Builder is a UKI file builder.
type Builder struct {
	// Source options.
	//
	// Arch of the UKI file.
	Arch string
	// Version of Talos.
	Version string
	// Path to the sd-stub.
	SdStubPath string
	// Path to the sd-boot.
	SdBootPath string
	// Path to the kernel image.
	KernelPath string
	// Path to the initrd image.
	InitrdPath string
	// Kernel cmdline.
	Cmdline string
	// Os-release file
	OsRelease string
	// SecureBoot certificate and signer.
	SecureBootSigner pesign.CertificateSigner
	// PCR signer.
	PCRSigner measure.RSAKey

	// Output options:
	//
	// Path to the signed sd-boot.
	OutSdBootPath string
	// Path to the output UKI file.
	OutUKIPath string

	// fields initialized during build
	sections        []section
	scratchDir      string
	peSigner        *pesign.Signer
	unsignedUKIPath string
}

// Build the UKI file.
//
// Build process is as follows:
//   - sign the sd-boot EFI binary, and write it to the OutSdBootPath
//   - build ephemeral sections (uname, os-release), and other proposed sections
//   - measure sections, generate signature, and append to the list of sections
//   - assemble the final UKI file starting from sd-stub and appending generated section.
func (builder *Builder) Build() error {
	var err error

	builder.scratchDir, err = os.MkdirTemp("", "ukify")
	if err != nil {
		return err
	}

	defer func() {
		if err = os.RemoveAll(builder.scratchDir); err != nil {
			log.Printf("failed to remove scratch dir: %v", err)
		}
	}()

	slog.Info("Signing systemd-boot")

	builder.peSigner, err = pesign.NewSigner(builder.SecureBootSigner)
	if err != nil {
		return fmt.Errorf("error initializing signer: %w", err)
	}

	// sign sd-boot
	if err = builder.peSigner.Sign(builder.SdBootPath, builder.OutSdBootPath); err != nil {
		return fmt.Errorf("error signing sd-boot: %w", err)
	}

	slog.Info("Generating UKI sections")

	// generate and build list of all sections
	for _, generateSection := range []func() error{
		builder.generateOSRel,
		builder.generateCmdline,
		builder.generateInitrd,
		builder.generateSplash,
		builder.generateUname,
		builder.generateSBAT,
		builder.generatePCRPublicKey,
		// append kernel last to account for decompression
		builder.generateKernel,
		// measure sections last
		builder.generatePCRSig,
	} {
		if err = generateSection(); err != nil {
			return fmt.Errorf("error generating sections: %w", err)
		}
	}

	slog.Info("Assembling UKI")

	// assemble the final UKI file
	if err = builder.assemble(); err != nil {
		return fmt.Errorf("error assembling UKI: %w", err)
	}

	slog.Info("Signing UKI")

	// sign the UKI file
	return builder.peSigner.Sign(builder.unsignedUKIPath, builder.OutUKIPath)
}