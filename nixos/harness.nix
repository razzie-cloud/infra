{ config, pkgs, ... }:

{
  ############################################################
  # Harness
  ############################################################
  virtualisation.docker.rootless = {
    enable = true;
    setSocketVariable = false; # don't override DOCKER_HOST for all users globally
    liveRestore = true;
    daemon.settings = {
      "data-root" = "/var/lib/docker-harness";
    };
  };
  users.groups.harness = { };
  users.users.harness = {
    isSystemUser = true;
    description  = "Harness CI runner user";
    group        = "harness";
    extraGroups  = [ ];
    home         = "/var/lib/harness";
    createHome   = true;
    linger       = true;
  };
  systemd.tmpfiles.rules = [
    "d /var/lib/docker-harness 0750 harness harness -"
  ];
  environment.loginShellInit = ''
    if [ "$USER" = "harness" ]; then
      export DOCKER_HOST="unix:///run/user/$(id -u)/docker.sock"
    fi
  '';
}
