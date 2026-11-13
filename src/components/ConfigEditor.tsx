import React, { ChangeEvent, useState } from 'react';

import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import {
  Alert,
  FieldSet,
  InlineField,
  InlineSwitch,
  Input,
  RadioButtonGroup,
  SecretInput,
  SecretTextArea,
} from '@grafana/ui';

import {
  AuthType,
  DEFAULT_MAX_ROWS,
  DEFAULT_PORT,
  DEFAULT_QUERY_TIMEOUT,
  HardchargerOptions,
  HardchargerSecureJsonData,
} from '../types';

type Props = DataSourcePluginOptionsEditorProps<HardchargerOptions, HardchargerSecureJsonData>;
type SecureKey = keyof HardchargerSecureJsonData;
type ConnectionMode = 'host' | 'uri';

const LABEL_WIDTH = 24;
const INPUT_WIDTH = 50;

const connectionModes: Array<{ label: string; value: ConnectionMode }> = [
  { label: 'Host and port', value: 'host' },
  { label: 'URI', value: 'uri' },
];

const authTypes: Array<{ label: string; value: AuthType; description: string }> = [
  { label: 'Username and password', value: 'password', description: 'Sign in with a user name and a password' },
  { label: 'Token', value: 'token', description: 'Use an access token issued for this data source' },
  {
    label: 'Certificate',
    value: 'client_certificate',
    description: 'Sign in with the TLS client certificate alone (the server must offer certificate sign-in)',
  },
];

/** A number field's value: nothing for an empty field, so the default applies. */
export function numberOrUndefined(text: string): number | undefined {
  if (text.trim() === '') {
    return undefined;
  }
  const value = Number(text);
  return Number.isFinite(value) ? value : undefined;
}

/** Whether a connection URI is TLS, as the driver reads it: yes, unless it is http:// or says tls=false. */
export function uriUsesTLS(uri: string): boolean {
  return !/^\s*http:\/\//i.test(uri) && !/[?&]tls=(false|0)(&|$)/i.test(uri.trim());
}

export function ConfigEditor({ options, onOptionsChange }: Props) {
  const { jsonData, secureJsonFields } = options;
  const secureJsonData = options.secureJsonData ?? {};
  const [mode, setMode] = useState<ConnectionMode>(jsonData.uri ? 'uri' : 'host');
  // The URI typed in this visit, so switching to "Host and port" and back does not lose it.
  const [uriDraft, setUriDraft] = useState(jsonData.uri ?? '');

  const setJson = (patch: Partial<HardchargerOptions>) =>
    onOptionsChange({ ...options, jsonData: { ...jsonData, ...patch } });

  const onText = (key: keyof HardchargerOptions) => (event: ChangeEvent<HTMLInputElement>) =>
    setJson({ [key]: event.currentTarget.value });

  const onNumber = (key: keyof HardchargerOptions) => (event: ChangeEvent<HTMLInputElement>) =>
    setJson({ [key]: numberOrUndefined(event.currentTarget.value) });

  const onSecret = (key: SecureKey) => (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
    onOptionsChange({ ...options, secureJsonData: { ...secureJsonData, [key]: event.currentTarget.value } });

  const onResetSecret = (key: SecureKey) => () =>
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, [key]: false },
      secureJsonData: { ...secureJsonData, [key]: '' },
    });

  const onModeChange = (next: ConnectionMode) => {
    setMode(next);
    // A saved URI wins over host and port, so it is only kept while its mode is chosen.
    setJson({ uri: next === 'uri' ? uriDraft : '' });
  };

  const authType: AuthType = jsonData.authType ?? 'password';
  const usesTLS = mode === 'uri' ? uriUsesTLS(jsonData.uri ?? '') : jsonData.useTLS !== false;

  const secret = (key: SecureKey, label: string, tooltip: string, placeholder: string) => (
    <InlineField label={label} labelWidth={LABEL_WIDTH} tooltip={tooltip} htmlFor={'hardcharger-' + key} interactive>
      <SecretInput
        id={'hardcharger-' + key}
        width={INPUT_WIDTH}
        isConfigured={Boolean(secureJsonFields?.[key])}
        value={secureJsonData[key] ?? ''}
        placeholder={placeholder}
        onChange={onSecret(key)}
        onReset={onResetSecret(key)}
      />
    </InlineField>
  );

  const pem = (key: SecureKey, label: string, tooltip: string, placeholder: string) => (
    <InlineField label={label} labelWidth={LABEL_WIDTH} tooltip={tooltip} htmlFor={'hardcharger-' + key} interactive>
      <SecretTextArea
        id={'hardcharger-' + key}
        cols={INPUT_WIDTH}
        rows={4}
        isConfigured={Boolean(secureJsonFields?.[key])}
        value={secureJsonData[key] ?? ''}
        placeholder={placeholder}
        onChange={onSecret(key)}
        onReset={onResetSecret(key)}
      />
    </InlineField>
  );

  return (
    <>
      <FieldSet label="Connection">
        <InlineField label="Connect with" labelWidth={LABEL_WIDTH}>
          <RadioButtonGroup options={connectionModes} value={mode} onChange={onModeChange} />
        </InlineField>
        {mode === 'uri' ? (
          <InlineField
            label="Connection URI"
            labelWidth={LABEL_WIDTH}
            htmlFor="hardcharger-uri"
            interactive
            tooltip="hardcharger://host[:port] connects over TLS; add ?tls=false for a server without TLS. The port defaults to 31337 (443 for a GizmoData Cloud host). Credentials do not go in the URI."
          >
            <Input
              id="hardcharger-uri"
              width={INPUT_WIDTH}
              value={jsonData.uri ?? ''}
              placeholder="hardcharger://db.example.com:31337"
              onChange={(event) => {
                setUriDraft(event.currentTarget.value);
                setJson({ uri: event.currentTarget.value });
              }}
            />
          </InlineField>
        ) : (
          <>
            <InlineField
              label="Host"
              labelWidth={LABEL_WIDTH}
              htmlFor="hardcharger-host"
              tooltip="The Hardcharger server's host name or IP address"
            >
              <Input
                id="hardcharger-host"
                width={INPUT_WIDTH}
                value={jsonData.host ?? ''}
                placeholder="db.example.com"
                onChange={onText('host')}
              />
            </InlineField>
            <InlineField
              label="Port"
              labelWidth={LABEL_WIDTH}
              htmlFor="hardcharger-port"
              tooltip={'Empty: ' + DEFAULT_PORT + ', or 443 for a GizmoData Cloud host'}
            >
              <Input
                id="hardcharger-port"
                type="number"
                min={1}
                max={65535}
                width={INPUT_WIDTH}
                value={jsonData.port ?? ''}
                placeholder={String(DEFAULT_PORT)}
                onChange={onNumber('port')}
              />
            </InlineField>
            <InlineField
              label="Use TLS"
              labelWidth={LABEL_WIDTH}
              htmlFor="hardcharger-use-tls"
              tooltip="Connect over HTTPS. Turn off only for a server that runs without TLS, such as a local development server."
            >
              <InlineSwitch
                id="hardcharger-use-tls"
                value={jsonData.useTLS !== false}
                onChange={(event) => setJson({ useTLS: event.currentTarget.checked })}
              />
            </InlineField>
          </>
        )}
        {!usesTLS && (
          <Alert severity="warning" title="This connection is not encrypted">
            Without TLS the credentials and every result cross the network in plain text. Use it only on a network you
            trust.
          </Alert>
        )}
      </FieldSet>

      <FieldSet label="Authentication">
        <InlineField label="Sign in with" labelWidth={LABEL_WIDTH}>
          <RadioButtonGroup options={authTypes} value={authType} onChange={(value) => setJson({ authType: value })} />
        </InlineField>
        {authType === 'password' && (
          <>
            <InlineField label="Username" labelWidth={LABEL_WIDTH} htmlFor="hardcharger-username">
              <Input
                id="hardcharger-username"
                width={INPUT_WIDTH}
                value={jsonData.username ?? ''}
                placeholder="grafana_reader"
                onChange={onText('username')}
              />
            </InlineField>
            {secret(
              'password',
              'Password',
              'Stored encrypted by Grafana and only sent to the plugin backend',
              'Password'
            )}
          </>
        )}
        {authType === 'token' &&
          secret(
            'token',
            'Access token',
            'A Hardcharger access token. Stored encrypted by Grafana and only sent to the plugin backend. The plugin cannot renew it.',
            'Access token'
          )}
        {authType === 'client_certificate' && (
          <Alert severity="info" title="Certificate sign-in">
            The client certificate and key under TLS below are the credential. The connection must use TLS, and the
            server must offer certificate sign-in.
          </Alert>
        )}
      </FieldSet>

      {usesTLS && (
        <FieldSet label="TLS">
          {pem(
            'tlsCACert',
            'CA certificate',
            'PEM certificates of the authority that signed the server certificate. Empty: the certificate authorities of the machine Grafana runs on.',
            '-----BEGIN CERTIFICATE-----'
          )}
          <InlineField
            label="TLS server name"
            labelWidth={LABEL_WIDTH}
            htmlFor="hardcharger-tls-server-name"
            tooltip="The name the server certificate is checked against, when it is not the host connected to"
          >
            <Input
              id="hardcharger-tls-server-name"
              width={INPUT_WIDTH}
              value={jsonData.tlsServerName ?? ''}
              placeholder="Same as the host"
              onChange={onText('tlsServerName')}
            />
          </InlineField>
          {pem(
            'tlsClientCert',
            'Client certificate',
            'PEM certificate chain presented to a server that asks for a client certificate (mutual TLS)',
            '-----BEGIN CERTIFICATE-----'
          )}
          {pem(
            'tlsClientKey',
            'Client key',
            "The client certificate's private key, PEM. Stored encrypted by Grafana and only sent to the plugin backend.",
            '-----BEGIN PRIVATE KEY-----'
          )}
          <InlineField
            label="Skip TLS verification"
            labelWidth={LABEL_WIDTH}
            htmlFor="hardcharger-tls-skip-verify"
            tooltip="Do not verify the server certificate. For development only."
          >
            <InlineSwitch
              id="hardcharger-tls-skip-verify"
              value={Boolean(jsonData.tlsSkipVerify)}
              onChange={(event) => setJson({ tlsSkipVerify: event.currentTarget.checked })}
            />
          </InlineField>
          {jsonData.tlsSkipVerify && (
            <Alert severity="warning" title="The server certificate is not verified">
              Anyone on the network path can pose as the server and read the credentials. Add the CA certificate
              instead.
            </Alert>
          )}
        </FieldSet>
      )}

      <FieldSet label="Queries">
        <InlineField
          label="Default catalog"
          labelWidth={LABEL_WIDTH}
          htmlFor="hardcharger-catalog"
          tooltip="The catalog unqualified table names are looked up in. Empty: the server's default."
        >
          <Input
            id="hardcharger-catalog"
            width={INPUT_WIDTH}
            value={jsonData.catalog ?? ''}
            placeholder="The server's default"
            onChange={onText('catalog')}
          />
        </InlineField>
        <InlineField
          label="Default schema"
          labelWidth={LABEL_WIDTH}
          htmlFor="hardcharger-schema"
          tooltip="One schema name in the default catalog. Empty: the server's default."
        >
          <Input
            id="hardcharger-schema"
            width={INPUT_WIDTH}
            value={jsonData.schema ?? ''}
            placeholder="The server's default"
            onChange={onText('schema')}
          />
        </InlineField>
        <InlineField
          label="Max rows"
          labelWidth={LABEL_WIDTH}
          htmlFor="hardcharger-max-rows"
          tooltip={
            'The most rows read from one query; a longer result is cut off, with a notice on the panel. Empty: ' +
            DEFAULT_MAX_ROWS.toLocaleString('en-US') +
            '.'
          }
        >
          <Input
            id="hardcharger-max-rows"
            type="number"
            min={1}
            width={INPUT_WIDTH}
            value={jsonData.maxRows ?? ''}
            placeholder={String(DEFAULT_MAX_ROWS)}
            onChange={onNumber('maxRows')}
          />
        </InlineField>
        <InlineField
          label="Query timeout (seconds)"
          labelWidth={LABEL_WIDTH}
          htmlFor="hardcharger-query-timeout"
          tooltip={
            'A query that runs longer is cancelled on the server. Empty: ' + DEFAULT_QUERY_TIMEOUT + '. 0: no limit.'
          }
        >
          <Input
            id="hardcharger-query-timeout"
            type="number"
            min={0}
            width={INPUT_WIDTH}
            value={jsonData.queryTimeout ?? ''}
            placeholder={String(DEFAULT_QUERY_TIMEOUT)}
            onChange={onNumber('queryTimeout')}
          />
        </InlineField>
        <InlineField
          label="Tag queries"
          labelWidth={LABEL_WIDTH}
          htmlFor="hardcharger-tag-queries"
          tooltip="Record with each statement which Grafana dashboard, panel, alert rule and user it came from, in the server's query tag"
        >
          <InlineSwitch
            id="hardcharger-tag-queries"
            value={jsonData.tagQueries !== false}
            onChange={(event) => setJson({ tagQueries: event.currentTarget.checked })}
          />
        </InlineField>
      </FieldSet>
    </>
  );
}
