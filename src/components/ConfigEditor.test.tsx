import React, { useState } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';

import { DataSourceSettings } from '@grafana/data';

import { HardchargerOptions, HardchargerSecureJsonData } from '../types';
import { ConfigEditor, numberOrUndefined, uriUsesTLS } from './ConfigEditor';

type Settings = DataSourceSettings<HardchargerOptions, HardchargerSecureJsonData>;

function settings(
  jsonData: HardchargerOptions = {},
  secureJsonFields: Record<string, boolean> = {},
  secureJsonData: HardchargerSecureJsonData = {}
): Settings {
  return {
    id: 1,
    uid: 'hc',
    orgId: 1,
    name: 'Hardcharger',
    type: 'gizmodata-hardcharger-datasource',
    access: 'proxy',
    readOnly: false,
    jsonData,
    secureJsonFields,
    secureJsonData,
  } as unknown as Settings;
}

/** The editor with the state Grafana keeps for it: every change is fed back in. */
function setup(initial: Settings) {
  let current = initial;
  const Harness = () => {
    const [options, setOptions] = useState(initial);
    current = options;
    return <ConfigEditor options={options} onOptionsChange={setOptions} />;
  };
  render(<Harness />);
  return { options: () => current };
}

function type(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe('ConfigEditor', () => {
  it('starts with host and port, TLS on, password sign-in and tagging on', () => {
    setup(settings());
    expect(screen.getByLabelText('Host')).toHaveValue('');
    expect(screen.getByLabelText('Port')).toHaveValue(null);
    expect(screen.getByLabelText('Port')).toHaveAttribute('placeholder', '31337');
    expect(screen.getByLabelText('Use TLS')).toBeChecked();
    expect(screen.getByLabelText('Username')).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.queryByLabelText('Access token')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Connection URI')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Tag queries')).toBeChecked();
    expect(screen.getByLabelText('Skip TLS verification')).not.toBeChecked();
    expect(screen.queryByText('This connection is not encrypted')).not.toBeInTheDocument();
  });

  it('saves every field', () => {
    const { options } = setup(settings());
    type('Host', 'db.example.com');
    type('Port', '8443');
    type('Username', 'grafana_reader');
    type('Password', 'p@ss');
    type('TLS server name', 'db.internal');
    type('CA certificate', '-----BEGIN CERTIFICATE-----\nCA');
    type('Client certificate', '-----BEGIN CERTIFICATE-----\nCLIENT');
    type('Client key', '-----BEGIN PRIVATE KEY-----\nKEY');
    type('Default catalog', 'lake');
    type('Default schema', 'reports');
    type('Max rows', '5000');
    type('Query timeout (seconds)', '45');
    fireEvent.click(screen.getByLabelText('Skip TLS verification'));
    fireEvent.click(screen.getByLabelText('Tag queries'));

    expect(options().jsonData).toEqual({
      host: 'db.example.com',
      port: 8443,
      username: 'grafana_reader',
      tlsServerName: 'db.internal',
      catalog: 'lake',
      schema: 'reports',
      maxRows: 5000,
      queryTimeout: 45,
      tlsSkipVerify: true,
      tagQueries: false,
    });
    expect(options().secureJsonData).toEqual({
      password: 'p@ss',
      tlsCACert: '-----BEGIN CERTIFICATE-----\nCA',
      tlsClientCert: '-----BEGIN CERTIFICATE-----\nCLIENT',
      tlsClientKey: '-----BEGIN PRIVATE KEY-----\nKEY',
    });
    expect(screen.getByText('The server certificate is not verified')).toBeInTheDocument();
  });

  it('keeps a secret out of jsonData', () => {
    const { options } = setup(settings());
    type('Password', 'p@ss');
    expect(JSON.stringify(options().jsonData)).not.toContain('p@ss');
  });

  it('clears a number field back to its default, and keeps a zero timeout', () => {
    const { options } = setup(settings({ host: 'h', port: 9000, maxRows: 10, queryTimeout: 30 }));
    type('Port', '');
    type('Max rows', '');
    type('Query timeout (seconds)', '0');
    expect(options().jsonData).toEqual({ host: 'h', port: undefined, maxRows: undefined, queryTimeout: 0 });
    expect(screen.getByLabelText('Query timeout (seconds)')).toHaveValue(0);
  });

  it('warns when TLS is turned off, and hides the TLS settings', () => {
    const { options } = setup(settings({ host: 'h' }));
    fireEvent.click(screen.getByLabelText('Use TLS'));
    expect(options().jsonData.useTLS).toBe(false);
    expect(screen.getByText('This connection is not encrypted')).toBeInTheDocument();
    expect(screen.queryByLabelText('CA certificate')).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText('Use TLS'));
    expect(options().jsonData.useTLS).toBe(true);
    expect(screen.getByLabelText('CA certificate')).toBeInTheDocument();
  });

  it('switches to a connection URI and back without losing either', () => {
    const { options } = setup(settings({ host: 'db.example.com', port: 31337 }));
    fireEvent.click(screen.getByLabelText('URI'));
    expect(screen.queryByLabelText('Host')).not.toBeInTheDocument();
    type('Connection URI', 'hardcharger://other.example.com?tls=false');
    expect(options().jsonData.uri).toBe('hardcharger://other.example.com?tls=false');
    expect(screen.getByText('This connection is not encrypted')).toBeInTheDocument();

    // Host and port: the saved URI is cleared, because it would win over them.
    fireEvent.click(screen.getByLabelText('Host and port'));
    expect(options().jsonData).toEqual({ host: 'db.example.com', port: 31337, uri: '' });
    expect(screen.getByLabelText('Host')).toHaveValue('db.example.com');

    fireEvent.click(screen.getByLabelText('URI'));
    expect(options().jsonData.uri).toBe('hardcharger://other.example.com?tls=false');
  });

  it('opens on the connection URI when one is saved', () => {
    setup(settings({ uri: 'hardcharger://db.example.com', host: 'ignored' }));
    expect(screen.getByLabelText('Connection URI')).toHaveValue('hardcharger://db.example.com');
    expect(screen.getByLabelText('URI')).toBeChecked();
    expect(screen.queryByLabelText('Host')).not.toBeInTheDocument();
    expect(screen.getByLabelText('CA certificate')).toBeInTheDocument();
  });

  it('switches the sign-in method, showing only its fields', () => {
    const { options } = setup(settings({ host: 'h', username: 'joe' }));
    fireEvent.click(screen.getByLabelText('Token'));
    expect(options().jsonData.authType).toBe('token');
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument();
    type('Access token', 'tok-123');
    expect(options().secureJsonData).toEqual({ token: 'tok-123' });

    fireEvent.click(screen.getByLabelText('Certificate'));
    expect(options().jsonData.authType).toBe('client_certificate');
    expect(screen.queryByLabelText('Access token')).not.toBeInTheDocument();
    expect(screen.getByText('Certificate sign-in')).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText('Username and password'));
    expect(options().jsonData.authType).toBe('password');
    expect(screen.getByLabelText('Username')).toHaveValue('joe');
  });

  // Everything set when the data source was created can be changed afterwards.
  it('shows saved secrets as configured and lets each one be replaced', () => {
    const { options } = setup(
      settings(
        { host: 'h', username: 'joe' },
        { password: true, tlsCACert: true, tlsClientCert: true, tlsClientKey: true }
      )
    );
    const resets = screen.getAllByRole('button', { name: 'Reset' });
    expect(resets).toHaveLength(4);
    expect(screen.getByLabelText('Password')).toBeDisabled();

    fireEvent.click(resets[0]);
    expect(options().secureJsonFields.password).toBe(false);
    expect(options().secureJsonData?.password).toBe('');
    expect(screen.getByLabelText('Password')).toBeEnabled();
    type('Password', 'new-password');
    expect(options().secureJsonData?.password).toBe('new-password');
    // The others are untouched.
    expect(options().secureJsonFields).toEqual({
      password: false,
      tlsCACert: true,
      tlsClientCert: true,
      tlsClientKey: true,
    });
    expect(screen.getAllByRole('button', { name: 'Reset' })).toHaveLength(3);
  });

  it('shows a saved token as configured and lets it be replaced', () => {
    const { options } = setup(settings({ host: 'h', authType: 'token' }, { token: true }));
    fireEvent.click(screen.getByRole('button', { name: 'Reset' }));
    type('Access token', 'tok-new');
    expect(options().secureJsonFields.token).toBe(false);
    expect(options().secureJsonData?.token).toBe('tok-new');
  });
});

describe('numberOrUndefined', () => {
  it('reads a number, and nothing from an empty or malformed field', () => {
    expect(numberOrUndefined('31337')).toBe(31337);
    expect(numberOrUndefined('0')).toBe(0);
    expect(numberOrUndefined('2.5')).toBe(2.5);
    expect(numberOrUndefined('')).toBeUndefined();
    expect(numberOrUndefined('  ')).toBeUndefined();
    expect(numberOrUndefined('abc')).toBeUndefined();
  });
});

describe('uriUsesTLS', () => {
  it('is TLS unless the URI is http or says tls=false', () => {
    expect(uriUsesTLS('hardcharger://h')).toBe(true);
    expect(uriUsesTLS('hardcharger://h:31337?tls=true')).toBe(true);
    expect(uriUsesTLS('https://h/chargewire')).toBe(true);
    expect(uriUsesTLS('')).toBe(true);
    expect(uriUsesTLS('hardcharger://h?tls=false')).toBe(false);
    expect(uriUsesTLS('hardcharger://h?tls=0')).toBe(false);
    expect(uriUsesTLS('http://h:31337')).toBe(false);
    expect(uriUsesTLS('HTTP://h')).toBe(false);
    expect(uriUsesTLS('hardcharger://h?tls=falsey')).toBe(true);
  });
});
