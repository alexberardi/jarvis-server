# The Windows 11 test VM

The desktop agent's live checks on Windows run in a QEMU/KVM VM on the dev box
(`docs/agent/DESKTOP-COMMANDS.md`, "Prerequisites"). `scripts/win11-vm` runs it as your own session
(no libvirt, no group change), with Secure Boot, a software TPM, PipeWire audio and two virtual
monitors. This page is the one-time setup.

## Once per box

```sh
sudo pacman -S --needed qemu-base swtpm edk2-ovmf passt qemu-audio-pipewire qemu-ui-gtk \
  qemu-hw-display-virtio-gpu qemu-hw-display-virtio-vga socat
```

- The Windows 11 ISO from Microsoft's download page (a browser; you need a license), in
  `~/Downloads` (or set `WIN11_ISO`).
- The virtio-win ISO in `~/vms/win11/virtio-win.iso` (or set `VIRTIO_ISO`):
  `https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso`.

Without `qemu-hw-display-virtio-vga` the script falls back to one standard VGA screen, and
move-to-monitor can't be tested.

## Install Windows

```sh
scripts/win11-vm install
```

1. Press a key at "Press any key to boot from CD or DVD".
2. Setup finds the disk without a driver (it's NVMe). Install on it.
3. At the network step there is no network yet (the virtio network driver isn't loaded), which
   is what makes a local account easy. If Setup insists on a Microsoft account, press Shift+F10
   and run `start ms-cxh:localonly`. Name the account `jarvis`.
4. Once at the desktop, open the virtio-win CD in Explorer and run `virtio-win-guest-tools.exe`:
   network, display (two monitors) and the balloon driver. Reboot.
5. Settings → System → Display: confirm two displays, "Extend these displays".

## Install the agent

The guest reaches this box's loopback as `10.0.2.2`, so the jarvisd on `127.0.0.1:7700` is
`http://10.0.2.2:7700` inside the VM. mDNS doesn't cross the NAT: always enter it by hand.

1. In the VM's browser, download the agent's Windows installer from the jarvisd release (or copy a
   dev build in: `scripts/win11-vm stop`, then share it over the jarvisd admin's Add-ons page).
2. Install, then in the setup page enter `http://10.0.2.2:7700` as the server and pair.
3. Check from the dev box: `scripts/win11-vm status`, and the computer shows in the app.

## Snapshot it

```sh
scripts/win11-vm stop
scripts/win11-vm snapshot clean
```

`clean` is Windows set up with the agent installed and paired. Revert before each test run:

```sh
scripts/win11-vm revert clean && scripts/win11-vm start
```

The snapshot covers the disk, the firmware variables and the TPM state together, so Windows'
BitLocker or Hello state never drifts from the disk.

## Notes

- `scripts/win11-vm start --headless` boots without a window: a quick check that the VM still
  starts after a QEMU update.
- `JARVIS_WIN11_DIR` must stay short (sockets live there): the default is
  `~/.cache/jarvis-harness/win11`.
- The steps above weren't run end to end yet: the script's boot path (Secure Boot firmware, TPM
  2.0, NVMe, network, audio) was checked headless on 2026-10-24; the Setup and agent steps are
  the first real install's to confirm, and to correct here.
