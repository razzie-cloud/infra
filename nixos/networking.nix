{ config, pkgs, ... }:

{
  networking = {
    useDHCP = false;
    interfaces.ens18.ipv4.addresses = [
      {
        address = "xxx.xxx.xxx.xxx";
        prefixLength = 24;
      }
    ];
    defaultGateway = "xxx.xxx.xxx.yyy";
  };
}