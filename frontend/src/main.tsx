import React from 'react';
import ReactDOM from 'react-dom/client';
import '@alfalab/core-components-themes/corp.css';
import './styles.css';
import { Entry } from './Entry';
import { createHttpApi } from './api';
import { createDemoApi } from './demo';

const mode = import.meta.env.VITE_DATA_MODE ?? 'demo';
if (mode !== 'demo' && mode !== 'api') throw new Error('VITE_DATA_MODE must be demo or api');
const api =
  mode === 'demo'
    ? createDemoApi(new URLSearchParams(window.location.search).get('scenario') ?? '')
    : createHttpApi(import.meta.env.VITE_API_BASE_URL ?? '');

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <Entry
      api={api}
      demo={mode === 'demo'}
      authStartUrl={import.meta.env.VITE_AUTH_START_URL ?? ''}
    />
  </React.StrictMode>,
);
