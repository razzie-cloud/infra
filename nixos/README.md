# NixOS host configuration

Optional host setup for razcloud: SSH access for a `deploy` user, Docker, a
firewall, automatic upgrades, and a conditional reboot timer. Deploying the
application stack is a separate step in the [infra quickstart](../README.md).

1. Start with a NixOS host and its generated `/etc/nixos/hardware-configuration.nix`.
   Adapt [configuration.nix](configuration.nix) and [networking.nix](networking.nix)
   for your disk, network interface, addresses, domain, and SSH keys. Copy the
   adapted files into `/etc/nixos/`, keeping the host's hardware configuration
   and existing `system.stateVersion`.
2. Apply the host configuration with `sudo nixos-rebuild switch` on that host.
3. On your local machine, create a Docker context using your SSH access:

   ```sh
   docker context create razcloud --docker "host=ssh://deploy@your-host"
   docker --context razcloud info
   ```

Initialize Swarm before deploying the stack. [harness.nix](harness.nix) is an
optional, separate runner setup and is not imported by the main configuration.
