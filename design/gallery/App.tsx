import './global.css';

import {
  AgentMessage,
  Button,
  Card,
  Description,
  FlowStep,
  Input,
  Label,
  Logo,
  Mono,
  Numeral,
  PromptComposer,
  StatusChip,
  Switch,
  TextField,
  Typography,
  LerrynProvider,
  WaveRule,
  useLerrynFonts,
  useLerrynTheme,
  type PromptContext,
} from '@lerryn/ui-native';
import { StatusBar } from 'expo-status-bar';
import { CalendarClock, Mail, Plus } from 'lucide-react-native';
import { useState, type ReactNode } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';

export default function App() {
  const [fontsLoaded] = useLerrynFonts();
  if (!fontsLoaded) return null;

  return (
    <SafeAreaProvider>
      <LerrynProvider>
        <Gallery />
      </LerrynProvider>
    </SafeAreaProvider>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <View className="gap-4">
      <Typography.Heading type="h5" className="text-ink-muted">
        {title}
      </Typography.Heading>
      {children}
    </View>
  );
}

function Gallery() {
  const { theme, colors, setTheme } = useLerrynTheme();
  const [prompt, setPrompt] = useState('');
  const [busy, setBusy] = useState(false);
  const [contexts, setContexts] = useState<PromptContext[]>([
    { id: 'feishu-base', label: '飞书多维表格' },
    { id: 'mail', label: '企业邮箱' },
  ]);

  return (
    <SafeAreaView style={{ flex: 1 }} edges={['top', 'left', 'right']}>
      <StatusBar style={theme === 'dark' ? 'light' : 'dark'} />
      <ScrollView contentContainerClassName="gap-10 px-4 pb-16 pt-4">
        <View className="flex-row items-center justify-between">
          <Logo variant="horizontal" size={36} />
          <View className="flex-row items-center gap-2">
            <Typography type="body-sm" color="muted">
              夜间
            </Typography>
            <Switch isSelected={theme === 'dark'} onSelectedChange={(on) => setTheme(on ? 'dark' : 'light')} />
          </View>
        </View>

        <Section title="本周">
          <Card>
            <Card.Body className="gap-3">
              <View className="flex-row items-end justify-between">
                <View>
                  <Numeral>128</Numeral>
                  <Typography type="body-sm" color="muted">
                    次自动运行，替你省下约 9.5 小时
                  </Typography>
                </View>
                <StatusChip status="succeeded" label="成功率 97%" />
              </View>
              <WaveRule />
            </Card.Body>
          </Card>
        </Section>

        <Section title="按钮">
          <View className="gap-3">
            <Button>
              <Plus size={18} color={colors['on-kraft']} strokeWidth={2.5} />
              <Button.Label>新建自动化</Button.Label>
            </Button>
            <View className="flex-row gap-3">
              <Button variant="secondary" containerClassName="flex-1">
                试运行
              </Button>
              <Button variant="outline" containerClassName="flex-1">
                查看日志
              </Button>
            </View>
            <View className="flex-row gap-3">
              <Button variant="ghost" containerClassName="flex-1">
                稍后再说
              </Button>
              <Button variant="danger" containerClassName="flex-1">
                删除流程
              </Button>
            </View>
            <Button isDisabled>连接中…</Button>
          </View>
        </Section>

        <Section title="运行状态">
          <View className="flex-row flex-wrap gap-2">
            <StatusChip status="queued" />
            <StatusChip status="running" />
            <StatusChip status="waiting" />
            <StatusChip status="succeeded" />
            <StatusChip status="failed" />
            <StatusChip status="paused" />
          </View>
        </Section>

        <Section title="流程">
          <View>
            <FlowStep
              index={1}
              kind="trigger"
              icon={CalendarClock}
              title="每个工作日 9:00"
              description="按北京时间触发"
              meta="0 9 * * 1-5"
              status="succeeded"
            />
            <FlowStep
              index={2}
              kind="agent"
              title="汇总昨天的新线索"
              description="读取飞书多维表格「线索池」，标出高意向客户"
              status="running"
            />
            <FlowStep index={3} kind="condition" title="有高意向客户时" meta="lead.score >= 80" status="queued" />
            <FlowStep
              index={4}
              kind="action"
              icon={Mail}
              title="发邮件给销售负责人"
              description="附上客户名单和跟进建议"
              status="waiting"
              isLast
            />
          </View>
        </Section>

        <Section title="对话">
          <View className="gap-4">
            <AgentMessage role="user">每天早上把昨天的新线索汇总一下，发给销售负责人</AgentMessage>
            <AgentMessage time="09:02">
              好的。我拆成了 4 步：定时触发、汇总线索、筛出高意向客户、发邮件。发送前需要你确认一次收件人。
            </AgentMessage>
            <AgentMessage isThinking={busy} time="09:03">
              收件人已改为销售部群组，下次运行在明天 9:00。
            </AgentMessage>
          </View>
          <PromptComposer
            value={prompt}
            onChangeText={setPrompt}
            isBusy={busy}
            onSubmit={() => {
              setBusy(true);
              setPrompt('');
              setTimeout(() => setBusy(false), 2400);
            }}
            onStop={() => setBusy(false)}
            contexts={contexts}
            onRemoveContext={(id) => setContexts((list) => list.filter((c) => c.id !== id))}
            onAddContext={() => setContexts((list) => [...list, { id: `crm-${list.length}`, label: '客户管理' }])}
            suggestions={['客户回复后提醒我', '每周五生成销售周报', '新订单同步到财务表']}
          />
        </Section>

        <Section title="表单">
          <TextField isRequired>
            <Label>流程名称</Label>
            <Input placeholder="例如：线索日报" defaultValue="线索日报" />
            <Description>会显示在运行记录和通知里</Description>
          </TextField>
          <TextField>
            <Label>触发时间</Label>
            <Input placeholder="0 9 * * 1-5" />
            <Description>
              Cron 表达式，例如 <Mono size="mono-sm">0 9 * * 1-5</Mono> 表示工作日 9 点
            </Description>
          </TextField>
        </Section>
      </ScrollView>
    </SafeAreaView>
  );
}
