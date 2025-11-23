{ config, pkgs, ... }:

{
  imports = [ ./hardware-configuration.nix ./networking.nix ];

  boot.loader.grub.device = "/dev/sda";
  environment.systemPackages = with pkgs; [ wget vim htop ];

  ############################################################
  # Basic system identity
  ############################################################
  networking.hostName = "razcloud";
  networking.domain = "razzie.cloud";
  networking.nameservers = [ "1.1.1.1" "8.8.8.8" ];
  time.timeZone = "Europe/Budapest";
  console.keyMap = "hu";

  ############################################################
  # SSH: key-only, no root login, no password/KbdInteractive
  ############################################################
  services.openssh.enable = true;
  services.openssh.settings = {
    PermitRootLogin = "no";
    PasswordAuthentication = false;
    KbdInteractiveAuthentication = false;
    PubkeyAuthentication = true;
  };

  ############################################################
  # User: non-root with SSH key + sudo + docker
  ############################################################
  users.users.deploy = {
    isNormalUser = true;
    extraGroups = [ "wheel" "docker" ];
    openssh.authorizedKeys.keys = [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIfO4r21esoA4EwsmErNVXZuQBoWyX3cKmfepQD7df/K"
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAIdyWV+FAdGf2Vn4sRSdcxAJjb1zJwiP1h1QS4sFV23"
    ];
  };
  security.sudo.wheelNeedsPassword = false;

  ############################################################
  # Docker
  ############################################################
  virtualisation.docker = {
    enable = true;
    enableOnBoot = true;
    #liveRestore = true; # doesn't work well with docker swarm
  };

  ############################################################
  # Firewall
  ############################################################
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [ 22 80 443 ];
  };

  ############################################################
  # Unattended security upgrades & basic hardening
  ############################################################
  # security.apparmor.enable = true;    # if supported by your kernel
  services.fail2ban.enable = true;      # simple SSH brute-force protection
  system.autoUpgrade = {
    enable = true;
    allowReboot = false;
  };
  # Run the built-in nixos-upgrade timer hourly (change if you like)
  systemd.timers."nixos-upgrade".timerConfig = {
    OnCalendar = "hourly";
    Persistent = true;   # catch up if the system was down
  };
  # Reboot at 03:00 only if a new system generation is pending
  systemd.services."nixos-conditional-reboot" = {
    description = "Reboot only if a new NixOS generation is pending";
    serviceConfig.Type = "oneshot";
    script = ''
      set -eu
      booted="$(readlink -f /run/booted-system || true)"
      current="$(readlink -f /run/current-system || true)"
      if [ -z "$booted" ] || [ -z "$current" ]; then
        echo "Cannot determine generations; skipping reboot."
        exit 0
      fi
      if [ "$booted" != "$current" ]; then
        echo "New generation active but not booted; rebooting now."
        /run/current-system/systemd/bin/systemctl reboot
      else
        echo "No reboot needed."
      fi
    '';
  };
  systemd.timers."nixos-conditional-reboot" = {
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnCalendar = "03:00";
      Persistent = true;
    };
  };

  ############################################################
  # NixOS release compatibility (do not change lightly)
  ############################################################
  system.stateVersion = "25.05";  # set to the version used at first install
}
