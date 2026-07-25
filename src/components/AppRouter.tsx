import OverviewApp from './OverviewApp';
import SessionPage from './SessionPage';

export default function AppRouter() {
  const path = typeof window === 'undefined' ? '/' : window.location.pathname;
  if (!path.startsWith('/sessions/')) return <OverviewApp />;

  return <SessionPage paneId={decodeURIComponent(path.slice('/sessions/'.length))} />;
}
