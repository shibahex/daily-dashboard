# The dashboard as a container (for Docker, Portainer…). Nix builds the server and the programs
# it runs (site/flake.nix, `container`); the image holds only those, the pages and the guides.
# Everything people save is in /app/site/data: keep it on a volume. See docs/dashboard.md.

FROM nixos/nix:2.24.14 AS build
WORKDIR /src
COPY site ./site
RUN nix --extra-experimental-features "nix-command flakes" build ./site#container ./site#timew-hook --out-link /result \
 && mkdir -p /rootfs/nix/store /rootfs/app/site/data /rootfs/tmp \
 && cp -a $(nix-store -qR /result /result-1) /rootfs/nix/store/ \
 && ln -s "$(readlink /result)/bin/health-dashboard-container" /rootfs/entrypoint \
 && ln -s "$(readlink /result-1)" /rootfs/app/site/.timew-hook \
 && cp -a site/web site/plan.example.json /rootfs/app/site/ \
 && chown -R 1000:1000 /rootfs/app/site/data && chmod 1777 /rootfs/tmp
COPY docs/README.md docs/dashboard.md docs/noctalia-plugin.md /rootfs/app/docs/
COPY docs/templates /rootfs/app/docs/templates

FROM scratch
COPY --from=build /rootfs /
ENV DASHBOARD_SITE=/app/site DASHBOARD_LISTEN=0.0.0.0:1628 HOME=/app/site/data/home
USER 1000:1000
VOLUME /app/site/data
EXPOSE 1628
ENTRYPOINT ["/entrypoint"]
CMD ["1628"]
