Name:           watchparty
Version:        %{?app_version}%{!?app_version:0.0.0}
Release:        1%{?dist}
Summary:        Reproducción compartida y sincronizada entre participantes
License:        %{app_license}
URL:            https://github.com/Rusysa/watch-party-app
BuildArch:      x86_64
Requires:       mpv
Requires:       gtk3
Requires:       webkit2gtk4.1

%description
Cliente de escritorio para ver un enlace HTTP/HTTPS en grupo mediante mpv,
con sincronización entre participantes usando WebRTC.

%prep

%build

%install
install -Dm755 %{_sourcedir}/watchparty %{buildroot}%{_bindir}/watchparty
install -Dm644 %{_sourcedir}/watchparty.desktop %{buildroot}%{_datadir}/applications/watchparty.desktop
install -Dm644 %{_sourcedir}/appicon.png %{buildroot}%{_datadir}/icons/hicolor/1024x1024/apps/watchparty.png
install -Dm644 %{_sourcedir}/credits.md %{buildroot}%{_docdir}/watchparty/credits.md
install -Dm644 %{_sourcedir}/README.md %{buildroot}%{_docdir}/watchparty/README.md
mkdir -p %{buildroot}%{_docdir}/watchparty/third-party
cp -a %{_sourcedir}/third-party/. %{buildroot}%{_docdir}/watchparty/third-party/
install -Dm644 %{_sourcedir}/LICENSE %{buildroot}%{_docdir}/watchparty/LICENSE

%files
%{_bindir}/watchparty
%{_datadir}/applications/watchparty.desktop
%{_datadir}/icons/hicolor/1024x1024/apps/watchparty.png
%doc %{_docdir}/watchparty/credits.md
%doc %{_docdir}/watchparty/README.md
%doc %{_docdir}/watchparty/third-party
%license %{_docdir}/watchparty/LICENSE

%changelog
* Wed Sep 23 2026 rusysa <vegetable.sose@gmail.com> - 0.0.0-1
- Initial Fedora RPM packaging
