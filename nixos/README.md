### How to use

1) Put the config files at `/etc/nixos/configuration.nix` and `/etc/nixos/networking.nix` (adjust placeholders)
2) Build & switch
   ```bash
   sudo nixos-rebuild switch
   ```
3) From your local machine, create a remote Docker context (uses your SSH key)
   ```bash
   docker context create razcloud --docker "host=ssh://deploy@razzie.cloud"
   docker context use razcloud
   docker ps
   ```
