# THE PLAN

* each container is a stripped-down wshd running as a systemd service

* running processes behaves the same as garden linux (wsh <-> wshd)

* container rootfs is initialized via machinectl clone, providing efficient
  copy-on-write.

* stream in/out will have to have an extra step for the tar/untarring, since
  systemd only supports copying dirs local to the server, and doing bind-mounts

* dynamic volumes api may be supported in the future via `machinectl bind`

* currently, a regular container user is not created. this theoretically would
  be done the same as it is in garden (useradd -R with root image)

* side-goal: push as much state as possible out of the server itself and into
  systemd? eg, get snapshotting for "free"
