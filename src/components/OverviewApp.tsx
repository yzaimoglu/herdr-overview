import { useDeferredValue, useEffect, useState, type FormEvent } from 'react';
import { Badge } from '@cloudflare/kumo/components/badge';
import { Banner } from '@cloudflare/kumo/components/banner';
import { Breadcrumbs } from '@cloudflare/kumo/components/breadcrumbs';
import { Button, RefreshButton } from '@cloudflare/kumo/components/button';
import { Dialog } from '@cloudflare/kumo/components/dialog';
import { DropdownMenu } from '@cloudflare/kumo/components/dropdown';
import { Empty } from '@cloudflare/kumo/components/empty';
import { Grid, GridItem } from '@cloudflare/kumo/components/grid';
import { Input } from '@cloudflare/kumo/components/input';
import { LayerCard } from '@cloudflare/kumo/components/layer-card';
import { Loader } from '@cloudflare/kumo/components/loader';
import { Select } from '@cloudflare/kumo/components/select';
import { Sidebar } from '@cloudflare/kumo/components/sidebar';
import { Table } from '@cloudflare/kumo/components/table';
import { Text } from '@cloudflare/kumo/components/text';
import {
  ArrowsClockwise,
  ArrowSquareOut,
  Check,
  Command,
  Crosshair,
  DotsThreeOutline,
  House,
  MagnifyingGlass,
  PaperPlaneTilt,
  Plus,
  Pulse,
  TerminalWindow,
  UserCircle,
  WarningCircle,
  X,
} from '@phosphor-icons/react';

type AgentStatus = 'working' | 'blocked' | 'idle' | 'done' | 'unknown';

type Agent = {
  name: string;
  agent: string;
  status: AgentStatus;
  workspaceId: string;
  paneId: string;
  terminalId: string;
  sessionId: string;
  cwd: string;
  focused: boolean;
};

type ActivityEvent = {
  action: string;
  agentName: string;
  agentKind: string;
  paneId: string;
  at: string;
};

type UsageWindow = {
  name: string;
  utilization: number;
  resets_at: string;
};

type UsageMetric = {
  label: string;
  value: string;
};

type ProviderUsage = {
  tool: string;
  available: boolean;
  windows?: UsageWindow[];
  metrics?: UsageMetric[];
  error?: string;
};

type Overview = {
  source: 'live' | 'demo';
  herdrAvailable: boolean;
  notice?: string;
  fetchedAt: string;
  agents: Agent[];
  activities: ActivityEvent[];
  stats: { total: number; working: number; blocked: number; ready: number };
  usage: { fetchedAt: string; providers: ProviderUsage[] };
};

const apiBase = import.meta.env.PUBLIC_API_BASE ?? '';

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  const body = (await response.json()) as T & { error?: string };
  if (!response.ok) throw new Error(body.error ?? `Request failed with status ${response.status}`);
  return body;
}

function statusLabel(status: AgentStatus) {
  return ({ working: 'Working', blocked: 'Needs input', idle: 'Ready', done: 'Done', unknown: 'Unknown' })[status];
}

function statusVariant(status: AgentStatus) {
  if (status === 'working') return 'info' as const;
  if (status === 'blocked') return 'warning' as const;
  if (status === 'done') return 'success' as const;
  return 'secondary' as const;
}

function initials(agent: Agent) {
  return (agent.name || agent.agent).split(/[-\s_]+/).map((part) => part[0]).join('').slice(0, 2).toUpperCase();
}

function logoSource(tool: string) {
  return {
    claude: '/agent-icons/anthropic.svg',
    codex: '/agent-icons/openai.svg',
    opencode: '/agent-icons/opencode.svg',
  }[tool.toLowerCase()];
}

function agentLogo(agent: Agent) {
  const source = logoSource(agent.agent);
  return source ? <img className="size-5 object-contain text-kumo-info" src={source} alt="" aria-hidden="true" /> : initials(agent);
}

function shortId(value: string) {
  return value.length <= 16 ? value : `${value.slice(0, 8)}...${value.slice(-5)}`;
}

function shortWorkspace(value: string) {
  const parts = value.split('/').filter(Boolean);
  return parts.length > 2 ? parts.slice(-2).join('/') : value;
}

function timeAgo(timestamp: string) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(timestamp).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

function activityLabel(event: ActivityEvent) {
  const name = event.agentName || event.agentKind;
  return ({
    prompt: `Prompt sent to ${name}`,
    started: `${name} started`,
    closed: `${name} session closed`,
    interrupted: `${name} interrupted`,
  }[event.action] ?? `${name} responded`);
}

function activityIcon(action: string) {
  if (action === 'prompt') return <PaperPlaneTilt size={15} weight="fill" />;
  if (action === 'started' || action === 'completed') return <Check size={15} weight="bold" />;
  return <Pulse size={15} weight="bold" />;
}

function providerLabel(tool: string) {
  return tool === 'opencode' ? 'OpenCode' : tool.charAt(0).toUpperCase() + tool.slice(1);
}

function resetLabel(timestamp: string) {
  const resetAt = new Date(timestamp);
  if (Number.isNaN(resetAt.getTime())) return 'Reset time unavailable';
  const minutes = Math.max(0, Math.ceil((resetAt.getTime() - Date.now()) / 60000));
  const relative = minutes < 1
    ? 'soon'
    : minutes < 60
      ? `${minutes}m`
      : minutes < 1440
        ? `${Math.ceil(minutes / 60)}h`
        : `${Math.ceil(minutes / 1440)}d`;
  const exact = new Intl.DateTimeFormat('de-DE', {
    day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: 'Europe/Berlin', timeZoneName: 'short',
  }).format(resetAt);
  return `Resets in ${relative} · ${exact}`;
}

function usageBarClass(utilization: number) {
  if (utilization >= 90) return 'bg-kumo-danger';
  if (utilization >= 75) return 'bg-kumo-warning';
  return 'bg-kumo-success';
}

function UsageCard({ provider }: { provider: ProviderUsage }) {
  return (
    <div className="flex min-w-0 flex-col gap-4 rounded-lg border border-kumo-line bg-kumo-base p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <div className="grid size-9 shrink-0 place-items-center rounded-full bg-kumo-info-tint p-2 text-kumo-info ring-1 ring-kumo-line">
            {logoSource(provider.tool) ? <img className="size-5 object-contain" src={logoSource(provider.tool)} alt="" aria-hidden="true" /> : <Text size="xs">{provider.tool.slice(0, 1).toUpperCase()}</Text>}
          </div>
          <div className="flex min-w-0 flex-col gap-1">
            <Text bold size="sm">{providerLabel(provider.tool)}</Text>
            <Text variant="secondary" size="xs">AI subscription usage</Text>
          </div>
        </div>
        <Badge variant={provider.available ? 'success' : 'warning'} appearance="dot">
          {provider.available ? 'Available' : 'Unavailable'}
        </Badge>
      </div>
      {provider.available ? (
        <>
          {provider.windows?.map((window) => {
            const utilization = Math.max(0, Math.min(100, window.utilization));
            return (
              <div className="flex flex-col gap-2" key={window.name}>
                <div className="flex items-center justify-between gap-3">
                  <Text size="sm">{window.name.replaceAll('_', ' ')}</Text>
                  <Text variant="mono-secondary">{Math.round(utilization)}%</Text>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-kumo-fill" role="progressbar" aria-label={`${providerLabel(provider.tool)} ${window.name} usage`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={utilization}>
                  <div className={`h-full rounded-full transition-[width] ${usageBarClass(utilization)}`} style={{ width: `${utilization}%` }} />
                </div>
                <Text variant="secondary" size="xs">{resetLabel(window.resets_at)}</Text>
              </div>
            );
          })}
          {provider.metrics && provider.metrics.length > 0 && (
            <dl className="grid grid-cols-2 gap-3">
              {provider.metrics.map((metric) => (
                <div key={metric.label}>
                  <Text variant="secondary" size="xs" as="dt">{metric.label}</Text>
                  <Text bold size="sm" as="dd">{metric.value}</Text>
                </div>
              ))}
            </dl>
          )}
          {!provider.windows?.length && !provider.metrics?.length && <Text variant="secondary" size="sm">No usage data reported.</Text>}
        </>
      ) : (
        <Text variant="secondary" size="sm">{provider.error || 'The usage service did not return data.'}</Text>
      )}
    </div>
  );
}

function StatCard({ label, value, detail, tone }: {
  label: string;
  value: number | string;
  detail: string;
  tone: 'brand' | 'success' | 'warning' | 'neutral';
}) {
  const toneClass = {
    brand: 'border-l-kumo-brand',
    success: 'border-l-kumo-success',
    warning: 'border-l-kumo-warning',
    neutral: 'border-l-kumo-line',
  }[tone];
  return (
    <LayerCard className={`border-l-4 p-4 ${toneClass}`}>
      <div className="flex min-h-24 flex-col justify-between gap-4">
        <Text variant="secondary" size="xs">{label}</Text>
        <div className="flex flex-col gap-1">
          <Text variant="heading2" as="p">{value}</Text>
          <Text variant="secondary" size="xs">{detail}</Text>
        </div>
      </div>
    </LayerCard>
  );
}

function AgentActions({ agent, onOpen, onInterrupt, onClose }: {
  agent: Agent;
  onOpen: () => void;
  onInterrupt: () => void;
  onClose: () => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenu.Trigger>
         <Button aria-label={`Actions for ${agent.name || agent.agent}`} shape="square" size="sm" variant="ghost" onClick={(event) => event.stopPropagation()}><DotsThreeOutline size={16} weight="fill" /></Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Content>
        <DropdownMenu.Item icon={<ArrowSquareOut size={15} />} onClick={onOpen}>Open session</DropdownMenu.Item>
        <DropdownMenu.Item icon={<Crosshair size={15} />} onClick={onInterrupt}>Interrupt</DropdownMenu.Item>
        <DropdownMenu.Separator />
        <DropdownMenu.Item icon={<X size={15} />} variant="danger" onClick={onClose}>Close pane</DropdownMenu.Item>
      </DropdownMenu.Content>
    </DropdownMenu>
  );
}

function SessionTable({ agents, search, filter, refreshing, emptyTitle, emptyDescription, onOpen, onInterrupt, onClose, onReload, onSearchChange, onFilterChange }: {
  agents: Agent[];
  search: string;
  filter: string;
  refreshing: boolean;
  emptyTitle: string;
  emptyDescription: string;
  onOpen: (agent: Agent) => void;
  onInterrupt: (agent: Agent) => void;
  onClose: (agent: Agent) => void;
  onReload: () => void;
  onSearchChange: (value: string) => void;
  onFilterChange: (value: string) => void;
}) {
  return (
    <>
      <div className="session-filter-bar border-b border-kumo-fill p-3">
        <div className="relative min-w-0 flex-1">
          <MagnifyingGlass size={16} className="session-search-icon pointer-events-none absolute top-1/2 z-10 -translate-y-1/2 text-kumo-subtle" />
          <Input type="search" aria-label="Search sessions" placeholder="Search sessions…" value={search} onChange={(event) => onSearchChange(event.target.value)} className="session-search-input" />
        </div>
        <div className="flex gap-2">
          <Select
            aria-label="Filter sessions by status"
            value={filter}
            onValueChange={(value) => onFilterChange(String(value))}
            items={[{ value: 'all', label: 'All statuses' }, { value: 'working', label: 'Working' }, { value: 'blocked', label: 'Needs input' }, { value: 'ready', label: 'Ready' }]}
            className="min-w-0 flex-1 md:w-40 md:flex-none"
          />
          <RefreshButton variant="secondary" aria-label="Reload sessions" loading={refreshing} onClick={onReload} />
        </div>
      </div>
      {agents.length === 0 ? <div className="p-4"><Empty size="sm" title={emptyTitle} description={emptyDescription} icon={<TerminalWindow size={28} />} /></div> : <>
      <div className="hidden min-w-0 overflow-x-auto sm:block [&_thead]:sticky [&_thead]:top-0 [&_thead]:z-10 [&_thead]:bg-kumo-base">
        <Table>
          <Table.Header>
            <Table.Row>
              <Table.Head>Agent</Table.Head>
              <Table.Head>Status</Table.Head>
              <Table.Head>Workspace</Table.Head>
              <Table.Head className="w-px whitespace-nowrap" sticky="right" />
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {agents.map((agent) => (
              <Table.Row
                key={agent.paneId}
                variant="default"
                className="cursor-pointer transition-colors hover:bg-kumo-tint"
                onClick={() => onOpen(agent)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault();
                    onOpen(agent);
                  }
                }}
                tabIndex={0}
              >
                <Table.Cell>
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="grid size-9 shrink-0 place-items-center rounded-full bg-kumo-info-tint p-2 text-xs font-semibold text-kumo-info ring-1 ring-kumo-line">{agentLogo(agent)}</div>
                    <div className="flex min-w-0 flex-col gap-1">
                      <Text bold size="sm" truncate>{agent.name || agent.agent}</Text>
                      <Text variant="mono-secondary" truncate>{agent.agent} &middot; {shortId(agent.sessionId || agent.paneId)}</Text>
                    </div>
                  </div>
                </Table.Cell>
                <Table.Cell><Badge variant={statusVariant(agent.status)} appearance="dot">{statusLabel(agent.status)}</Badge></Table.Cell>
                <Table.Cell><Text variant="mono-secondary" truncate>{shortWorkspace(agent.cwd || agent.workspaceId)}</Text></Table.Cell>
                <Table.Cell className="text-right" sticky="right">
                  <AgentActions agent={agent} onOpen={() => onOpen(agent)} onInterrupt={() => onInterrupt(agent)} onClose={() => onClose(agent)} />
                </Table.Cell>
              </Table.Row>
            ))}
          </Table.Body>
        </Table>
      </div>

      <ul className="divide-y divide-kumo-fill sm:hidden">
        {agents.map((agent) => (
          <li key={agent.paneId} className="space-y-3 px-4 py-4 transition-colors hover:bg-kumo-tint">
            <div className="flex items-start gap-3">
              <button type="button" className="flex min-w-0 flex-1 items-center gap-3 text-left" onClick={() => onOpen(agent)}>
                <div className="grid size-9 shrink-0 place-items-center rounded-full bg-kumo-info-tint p-2 text-xs font-semibold text-kumo-info ring-1 ring-kumo-line">{agentLogo(agent)}</div>
                <div className="flex min-w-0 flex-col gap-1">
                  <Text bold size="sm" truncate>{agent.name || agent.agent}</Text>
                  <Text variant="mono-secondary" truncate>{agent.agent} &middot; {shortId(agent.sessionId || agent.paneId)}</Text>
                </div>
              </button>
              <AgentActions agent={agent} onOpen={() => onOpen(agent)} onInterrupt={() => onInterrupt(agent)} onClose={() => onClose(agent)} />
            </div>
            <dl className="grid grid-cols-2 gap-3 pl-11">
              <div className="min-w-0">
                <Text variant="secondary" size="xs" as="dt">Status</Text>
                <div className="mt-1"><Badge variant={statusVariant(agent.status)} appearance="dot">{statusLabel(agent.status)}</Badge></div>
              </div>
              <div className="min-w-0">
                <Text variant="secondary" size="xs" as="dt">Workspace</Text>
                <Text variant="mono-secondary" as="dd" truncate>{shortWorkspace(agent.cwd || agent.workspaceId)}</Text>
              </div>
            </dl>
          </li>
        ))}
      </ul>
      </>}
      <div className="flex items-center justify-between gap-2 border-t border-kumo-fill p-3">
        <Text variant="secondary" size="xs" aria-live="polite">{agents.length} visible session{agents.length === 1 ? '' : 's'}</Text>
        <Text variant="secondary" size="xs" DANGEROUS_className="hidden sm:inline-block">Auto-sync every 8s</Text>
      </div>
    </>
  );
}

export default function OverviewApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState('all');
  const [error, setError] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const [showStart, setShowStart] = useState(false);
  const [startForm, setStartForm] = useState({ name: '', kind: 'opencode', pane: '', args: '' });
  const deferredSearch = useDeferredValue(search.trim().toLowerCase());

  async function refresh() {
    setRefreshing(true);
    try {
      const next = await api<Overview>('/api/overview');
      setOverview(next);
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not connect to the overview service.');
    } finally {
      setRefreshing(false);
    }
  }

  function openAgent(agent: Agent) {
    window.location.assign(`/sessions/${encodeURIComponent(agent.paneId)}`);
  }

  async function runAgentAction(agent: Agent, action: 'interrupt' | 'close') {
    if (action === 'close' && !window.confirm(`Close the ${agent.name || agent.agent} session?`)) return;
    try {
      await api(`/api/agents/${encodeURIComponent(agent.paneId)}/${action}`, { method: 'POST' });
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `Could not ${action} the session.`);
    }
  }

  async function startAgent(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await api('/api/agents', {
        method: 'POST',
        body: JSON.stringify({
          name: startForm.name.trim(),
          kind: startForm.kind,
          pane: startForm.pane.trim(),
          args: startForm.args.trim() ? startForm.args.trim().split(/\s+/) : [],
        }),
      });
      setShowStart(false);
      setStartForm({ name: '', kind: 'opencode', pane: '', args: '' });
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not start the agent.');
    }
  }

  useEffect(() => {
    void refresh();
    const interval = window.setInterval(() => void refresh(), 8000);
    return () => window.clearInterval(interval);
  }, []);

  const agents = overview?.agents.filter((agent) => {
    const matchesFilter = filter === 'all'
      || (filter === 'working' && agent.status === 'working')
      || (filter === 'blocked' && agent.status === 'blocked')
      || (filter === 'ready' && (agent.status === 'idle' || agent.status === 'done'));
    const haystack = `${agent.name} ${agent.agent} ${agent.cwd} ${agent.status}`.toLowerCase();
    return matchesFilter && (!deferredSearch || haystack.includes(deferredSearch));
  }) ?? [];

  return (
    <Sidebar.Provider collapsible="offcanvas" mobileBreakpoint={760} className="h-screen min-h-screen bg-kumo-canvas">
      <Sidebar className="h-screen shrink-0">
        <Sidebar.Header className="gap-3 border-b border-kumo-hairline px-4 py-4">
          <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-kumo-brand text-white"><Pulse size={18} weight="bold" /></div>
          <div className="min-w-0 flex flex-col">
            <Text bold size="sm" truncate>herdr overview</Text>
            <Text variant="secondary" size="xs" truncate>Local control plane</Text>
          </div>
        </Sidebar.Header>
        <Sidebar.Content>
          <Sidebar.Group>
            <Sidebar.GroupLabel>Workspace</Sidebar.GroupLabel>
            <Sidebar.Menu>
              <Sidebar.MenuButton icon={<Command size={17} />} active>Overview</Sidebar.MenuButton>
            </Sidebar.Menu>
          </Sidebar.Group>
          <Sidebar.Group>
            <Sidebar.GroupLabel>Actions</Sidebar.GroupLabel>
            <Sidebar.Menu>
              <Sidebar.MenuButton icon={<Plus size={17} />} onClick={() => setShowStart(true)}>Start an agent</Sidebar.MenuButton>
              <Sidebar.MenuButton icon={<ArrowsClockwise size={17} />} onClick={() => void refresh()}>Sync now</Sidebar.MenuButton>
            </Sidebar.Menu>
          </Sidebar.Group>
        </Sidebar.Content>
        <Sidebar.Footer className="flex flex-col gap-2 border-t border-kumo-hairline p-3">
          <div className="flex items-center gap-2">
            <Badge variant={overview?.source === 'live' ? 'success' : 'warning'} appearance="dot">{overview?.source === 'live' ? 'Live' : 'Demo'}</Badge>
            <Text variant="secondary" size="xs">Herdr control</Text>
          </div>
          <Sidebar.Trigger />
        </Sidebar.Footer>
      </Sidebar>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-50 border-b border-kumo-hairline bg-kumo-canvas backdrop-blur-md">
          <div className="flex min-h-14 items-center justify-between gap-4 px-4 md:px-8">
            <div className="flex min-w-0 items-center gap-3">
              <Sidebar.Trigger />
              <Breadcrumbs size="sm">
                <Breadcrumbs.Link href="/" icon={<House size={14} />}>Workspace</Breadcrumbs.Link>
                <Breadcrumbs.Separator />
                <Breadcrumbs.Current loading={!overview}>Overview</Breadcrumbs.Current>
              </Breadcrumbs>
            </div>
            <div className="flex items-center gap-2">
              <Text variant="secondary" size="xs" DANGEROUS_className="hidden md:inline-block">{overview ? `Updated ${timeAgo(overview.fetchedAt)}` : 'Connecting...'}</Text>
              <RefreshButton aria-label="Refresh overview" size="sm" variant="ghost" loading={refreshing} onClick={() => void refresh()} />
              <Button aria-label="Open profile" shape="circle" size="sm" variant="secondary"><UserCircle size={18} /></Button>
            </div>
          </div>
        </header>

        <main className="mx-auto flex w-full max-w-[1440px] flex-1 flex-col gap-6 p-4 md:p-8">
          <section className="flex flex-col justify-between gap-4 md:flex-row md:items-end">
            <div className="flex max-w-2xl flex-col gap-2">
              <Text variant="secondary" size="sm">Agent operations</Text>
              <Text variant="heading1" as="h1">Good morning, operator.</Text>
              <Text variant="secondary" size="sm" as="p">Inspect active Herdr sessions, respond to blocked agents, and keep local work moving from one control surface.</Text>
             </div>
             <div className="flex gap-2">
              <Button variant="primary" icon={Plus} onClick={() => setShowStart(true)}>Start agent</Button>
             </div>
          </section>

          {error && <Banner variant="error" icon={<WarningCircle size={20} />} title="Action failed" description={error} action={<Button shape="square" size="sm" variant="ghost" aria-label="Dismiss error" onClick={() => setError('')}><X size={16} /></Button>} />}
          {overview?.notice && <Banner variant="alert" icon={<WarningCircle size={20} />} title="Demo data" description={overview.notice} />}

          <Grid variant="side-by-side" gap="sm" className="lg:grid-cols-4">
            <GridItem><StatCard label="Total agents" value={overview?.stats.total ?? '-'} detail="Across active workspaces" tone="brand" /></GridItem>
            <GridItem><StatCard label="Working" value={overview?.stats.working ?? '-'} detail="Currently processing" tone="success" /></GridItem>
            <GridItem><StatCard label="Needs input" value={overview?.stats.blocked ?? '-'} detail="Waiting for a response" tone="warning" /></GridItem>
            <GridItem><StatCard label="Ready" value={overview?.stats.ready ?? '-'} detail="Idle or completed" tone="neutral" /></GridItem>
          </Grid>

          <LayerCard render={<section id="usage" />} className="min-w-0 overflow-hidden p-4 md:p-5">
            <div className="usage-section-header flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
              <div className="flex flex-col gap-1">
                <Text variant="heading3" as="h2">AI usage</Text>
                <Text variant="secondary" size="xs">Subscription windows and recent provider activity.</Text>
              </div>
              {overview?.usage && <Text variant="secondary" size="xs">Updated {timeAgo(overview.usage.fetchedAt)}</Text>}
            </div>
            {!overview ? <div className="flex justify-center py-8"><Loader aria-label="Loading AI usage" /></div> : (
              <div className="grid gap-3 lg:grid-cols-3">
                {overview.usage.providers.map((provider) => <UsageCard key={provider.tool} provider={provider} />)}
              </div>
            )}
          </LayerCard>

          <Grid variant="2-1" gap="sm">
            <GridItem className="min-w-0">
              <LayerCard render={<section id="sessions" />} className="min-w-0 overflow-hidden p-0">
                <div className="flex flex-col gap-4 border-b border-kumo-hairline p-4 md:flex-row md:items-center md:justify-between">
                   <div className="flex flex-col gap-1">
                     <Text variant="heading3" as="h2">Agent sessions</Text>
                     <Text variant="secondary" size="xs">Select a session to inspect output or send a prompt.</Text>
                   </div>
                 </div>
                  <SessionTable
                    agents={agents}
                    search={search}
                   filter={filter}
                   refreshing={refreshing}
                   emptyTitle={overview ? 'No sessions found' : 'Loading sessions'}
                   emptyDescription={overview ? 'Try a different filter or search term.' : 'Reading the current Herdr workspace.'}
                   onOpen={(agent) => void openAgent(agent)}
                   onInterrupt={(agent) => void runAgentAction(agent, 'interrupt')}
                   onClose={(agent) => void runAgentAction(agent, 'close')}
                   onReload={() => void refresh()}
                   onSearchChange={setSearch}
                   onFilterChange={setFilter}
                 />
              </LayerCard>
            </GridItem>

            <GridItem className="min-w-0">
              <LayerCard render={<section id="activity" />} className="min-w-0 overflow-hidden p-0">
                <div className="flex items-center justify-between border-b border-kumo-hairline p-4">
                  <div className="flex flex-col gap-1">
                    <Text variant="heading3" as="h2">Recent activity</Text>
                    <Text variant="secondary" size="xs">Control plane events</Text>
                  </div>
                  <Pulse size={18} className="text-kumo-subtle" />
                </div>
                <div className="divide-y divide-kumo-hairline px-4">
                  {overview?.activities.slice(0, 6).map((event, index) => (
                     <div className="flex items-center gap-3 py-4" key={`${event.at}-${index}`}>
                       <div className="flex size-9 shrink-0 items-center justify-center rounded-full bg-kumo-info-tint p-1 text-kumo-info ring-1 ring-kumo-line">{activityIcon(event.action)}</div>
                      <div className="flex min-w-0 flex-col gap-1">
                        <Text size="sm" truncate>{activityLabel(event)}</Text>
                        <Text variant="secondary" size="xs">{event.paneId} &middot; {timeAgo(event.at)}</Text>
                      </div>
                    </div>
                  ))}
                  {!overview && <div className="flex justify-center py-10"><Loader aria-label="Loading activity" /></div>}
                  {overview?.activities.length === 0 && <Empty size="sm" title="No activity yet" description="Agent actions will appear here." />}
                </div>
              </LayerCard>
            </GridItem>
          </Grid>

        </main>
      </div>

      <Dialog.Root open={showStart} onOpenChange={setShowStart}>
        <Dialog size="base" className="flex flex-col gap-5 p-6">
          <div>
            <Dialog.Title>Start an agent</Dialog.Title>
            <Dialog.Description>Open a new Herdr session in an existing pane.</Dialog.Description>
          </div>
          <form className="flex flex-col gap-4" onSubmit={startAgent}>
            <Input label="Name" required placeholder="researcher" value={startForm.name} onChange={(event) => setStartForm({ ...startForm, name: event.target.value })} />
            <Select label="Agent kind" value={startForm.kind} onValueChange={(value) => setStartForm({ ...startForm, kind: String(value) })}>
              <Select.Option value="opencode">OpenCode</Select.Option>
              <Select.Option value="claude">Claude</Select.Option>
              <Select.Option value="codex">Codex</Select.Option>
            </Select>
            <Input label="Pane" required placeholder="w1:p2" value={startForm.pane} onChange={(event) => setStartForm({ ...startForm, pane: event.target.value })} />
            <Input label="Arguments" placeholder="--model sonnet" value={startForm.args} onChange={(event) => setStartForm({ ...startForm, args: event.target.value })} />
            <div className="flex justify-end gap-2 pt-2">
              <Dialog.Close render={<Button variant="ghost">Cancel</Button>} />
              <Button type="submit" variant="primary" icon={Plus}>Start agent</Button>
            </div>
          </form>
        </Dialog>
      </Dialog.Root>
    </Sidebar.Provider>
  );
}
