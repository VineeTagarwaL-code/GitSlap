flake:

{ config, lib, pkgs, ... }:

let
  cfg = config.programs.gitslap;
  gitslapPkg = flake.packages.${pkgs.stdenv.hostPlatform.system}.default;
in
{
  options.programs.gitslap = {
    enable = lib.mkEnableOption "gitslap - git automation via physical gestures";

    package = lib.mkOption {
      type = lib.types.package;
      default = gitslapPkg;
      defaultText = lib.literalExpression "inputs.gitslap.packages.\${system}.default";
      description = "The gitslap package to use.";
    };

    repo = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to the git repo to operate on.";
    };

    fast = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable faster detection tuning.";
    };

    minAmplitude = lib.mkOption {
      type = lib.types.nullOr lib.types.float;
      default = null;
      description = "Minimum amplitude threshold (0.0-1.0).";
    };

    cooldown = lib.mkOption {
      type = lib.types.nullOr lib.types.int;
      default = null;
      description = "Cooldown between responses in milliseconds.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
