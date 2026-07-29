import React, { useCallback, useMemo, useState } from 'react';
import { Banner, TagInput, Typography } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';

const { Text } = Typography;

function parsePatterns(str) {
  if (!str || !str.trim()) return [];
  try {
    const parsed = JSON.parse(str);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((item) => typeof item === 'string');
  } catch {
    return [];
  }
}

// 空名单必须序列化成 "[]" 而不是 ""：后端配置系统的切片字段走 json.Unmarshal，
// 空串会解析失败并保留旧值，导致「清空名单」静默不生效。
function serializePatterns(patterns) {
  return JSON.stringify(patterns);
}

export function validatePattern(pattern) {
  const trimmed = (pattern || '').trim();
  if (!trimmed) return 'empty';
  if (trimmed === '*') return 'bare';
  const starCount = (trimmed.match(/\*/g) || []).length;
  if (starCount > 1) return 'multi';
  if (starCount === 1 && !trimmed.endsWith('*')) return 'position';
  return null;
}

export default function IgnoreGroupRatioModels({ value, onChange }) {
  const { t } = useTranslation();
  const [patterns, setPatterns] = useState(() => parsePatterns(value));

  const emitChange = useCallback(
    (next) => {
      setPatterns(next);
      onChange?.(serializePatterns(next));
    },
    [onChange],
  );

  const handleChange = useCallback(
    (next) => {
      // TagInput 允许重复输入，这里顺手去重去空格
      const cleaned = [];
      for (const item of next) {
        const trimmed = (item || '').trim();
        if (trimmed && !cleaned.includes(trimmed)) cleaned.push(trimmed);
      }
      emitChange(cleaned);
    },
    [emitChange],
  );

  const invalid = useMemo(
    () =>
      patterns
        .map((p) => ({ pattern: p, reason: validatePattern(p) }))
        .filter((item) => item.reason),
    [patterns],
  );

  const invalidMessage = useMemo(() => {
    if (!invalid.length) return null;
    const bare = invalid.find((item) => item.reason === 'bare');
    if (bare) {
      return t(
        '不允许使用裸 "*"，那会让全站所有模型都不计分组倍率；请写成具体前缀，例如 gpt-image-2*',
      );
    }
    return t('通配符 * 只能出现一次且必须在结尾，请检查：{{list}}', {
      list: invalid.map((item) => item.pattern).join('、'),
    });
  }, [invalid, t]);

  return (
    <div>
      <TagInput
        value={patterns}
        onChange={handleChange}
        placeholder={t('输入模型名后回车添加，支持前缀通配如 gpt-image-2*')}
        addOnBlur
        showClear
        style={{ width: '100%' }}
      />
      {invalidMessage && (
        <Banner
          type='danger'
          description={invalidMessage}
          closeIcon={null}
          style={{ marginTop: 8 }}
        />
      )}
      <Text
        type='tertiary'
        size='small'
        style={{ display: 'block', marginTop: 8, lineHeight: 1.8 }}
      >
        {t(
          '写法：精确模型名（如 dall-e-3），或以 * 结尾的前缀（如 gpt-image-2*，可一次覆盖 -1k/-2k/-4k/-pool 等整族，以后新增档位无需再改这里）。',
        )}
      </Text>
      <Text
        type='tertiary'
        size='small'
        style={{ display: 'block', marginTop: 4, lineHeight: 1.8 }}
      >
        {t(
          '命中的模型：分组倍率按 1.0 计，GroupRatio 与分组特殊倍率都不再生效，账单里显示的分组倍率也是 1。适用于按次计价的模型（生图等）——它们卖的是一次调用，不该被 token 分组折扣稀释。',
        )}
      </Text>
    </div>
  );
}
