import {
  Description,
  FieldError,
  Input,
  Label,
  ListBox,
  NumberField,
  SearchField,
  Select,
  Switch,
  TextArea,
  TextField,
} from "@heroui/react";
import { useId, type ComponentProps } from "react";

export interface Option {
  value: string;
  label: string;
  disabled?: boolean;
}
export function Choice({
  label,
  value,
  onChange,
  options,
  description,
  disabled,
  required,
  surface = true,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: Option[];
  description?: string;
  disabled?: boolean;
  required?: boolean;
  surface?: boolean;
}) {
  return (
    <Select
      className="w-full min-w-0"
      aria-label={label}
      value={value || null}
      onChange={(key) => onChange(key == null ? "" : String(key))}
      isRequired={required}
      isDisabled={disabled}
      placeholder="请选择"
      variant={surface ? "secondary" : "primary"}
    >
      <Label>{label}</Label>
      <Select.Trigger>
        <Select.Value />
        <Select.Indicator />
      </Select.Trigger>
      <Select.Popover>
        <ListBox disabledKeys={options.filter((option) => option.disabled).map((option) => option.value)}>
          {options.map((option) => (
            <ListBox.Item key={option.value} id={option.value} textValue={option.label}>
              {option.label}
              <ListBox.ItemIndicator />
            </ListBox.Item>
          ))}
        </ListBox>
      </Select.Popover>
      {description && <Description>{description}</Description>}
      <FieldError />
    </Select>
  );
}

export function MultiChoice({
  label,
  values,
  onChange,
  options,
  description,
  disabled,
  required,
  surface = true,
}: {
  label: string;
  values: string[];
  onChange: (values: string[]) => void;
  options: Option[];
  description?: string;
  disabled?: boolean;
  required?: boolean;
  surface?: boolean;
}) {
  return (
    <Select
      className="w-full min-w-0"
      aria-label={label}
      selectionMode="multiple"
      value={values}
      onChange={(keys) => onChange(Array.isArray(keys) ? keys.map(String) : [])}
      isRequired={required}
      isDisabled={disabled}
      placeholder="请选择"
      variant={surface ? "secondary" : "primary"}
    >
      <Label>{label}</Label>
      <Select.Trigger>
        <Select.Value />
        <Select.Indicator />
      </Select.Trigger>
      <Select.Popover>
        <ListBox
          selectionMode="multiple"
          disabledKeys={options.filter((option) => option.disabled).map((option) => option.value)}
        >
          {options.map((option) => (
            <ListBox.Item key={option.value} id={option.value} textValue={option.label}>
              {option.label}
              <ListBox.ItemIndicator />
            </ListBox.Item>
          ))}
        </ListBox>
      </Select.Popover>
      {description && <Description>{description}</Description>}
      <FieldError />
    </Select>
  );
}

export function Field({
  label,
  value,
  onChange,
  description,
  required,
  disabled,
  type = "text",
  placeholder,
  maxLength,
  min,
  max,
  step,
  autoComplete,
  surface = true,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  description?: string;
  required?: boolean;
  disabled?: boolean;
  type?: ComponentProps<typeof Input>["type"];
  placeholder?: string;
  maxLength?: number;
  min?: number;
  max?: number;
  step?: number;
  autoComplete?: string;
  surface?: boolean;
}) {
  return (
    <TextField className="w-full min-w-0" value={value} onChange={onChange} isRequired={required} isDisabled={disabled}>
      <Label>{label}</Label>
      <Input
        variant={surface ? "secondary" : "primary"}
        type={type}
        placeholder={placeholder}
        maxLength={maxLength}
        min={min}
        max={max}
        step={step}
        autoComplete={autoComplete}
      />
      {description && <Description>{description}</Description>}
      <FieldError />
    </TextField>
  );
}
export function TextFieldArea({
  label,
  value,
  onChange,
  description,
  required,
  rows = 5,
  maxLength,
  code,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  description?: string;
  required?: boolean;
  rows?: number;
  maxLength?: number;
  code?: boolean;
}) {
  return (
    <TextField className="w-full min-w-0" value={value} onChange={onChange} isRequired={required}>
      <Label>{label}</Label>
      <TextArea
        variant="secondary"
        rows={rows}
        maxLength={maxLength}
        className={code ? "w-full font-mono text-xs" : "w-full"}
        spellCheck={!code}
      />
      {description && <Description>{description}</Description>}
      <FieldError />
    </TextField>
  );
}
export function NumericField({
  label,
  value,
  onChange,
  description,
  required,
  disabled,
  min,
  max,
  step,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  description?: string;
  required?: boolean;
  disabled?: boolean;
  min?: number;
  max?: number;
  step?: number;
}) {
  const numberValue = value === "" ? undefined : Number(value);
  return (
    <NumberField
      className="w-full min-w-0"
      value={Number.isFinite(numberValue) ? numberValue : undefined}
      onChange={(next) => onChange(next == null ? "" : String(next))}
      isRequired={required}
      isDisabled={disabled}
      minValue={min}
      maxValue={max}
      step={step}
      variant="secondary"
    >
      <Label>{label}</Label>
      <NumberField.Group>
        <NumberField.DecrementButton />
        <NumberField.Input />
        <NumberField.IncrementButton />
      </NumberField.Group>
      {description && <Description>{description}</Description>}
      <FieldError />
    </NumberField>
  );
}

export function Toggle({
  label,
  selected,
  onChange,
  description,
  disabled,
}: {
  label: string;
  selected: boolean;
  onChange: (value: boolean) => void;
  description?: string;
  disabled?: boolean;
}) {
  const id = useId();
  return (
    <div className="space-y-2">
      <Switch
        isSelected={selected}
        onChange={onChange}
        isDisabled={disabled}
        aria-describedby={description ? id : undefined}
      >
        <Switch.Content>
          <Switch.Control>
            <Switch.Thumb />
          </Switch.Control>
          {label}
        </Switch.Content>
      </Switch>
      {description && (
        <p id={id} className="text-sm leading-6 text-muted">
          {description}
        </p>
      )}
    </div>
  );
}
export function Search({
  label,
  value,
  onChange,
  surface = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  surface?: boolean;
}) {
  return (
    <SearchField
      className="w-full"
      aria-label={label}
      value={value}
      onChange={onChange}
      variant={surface ? "secondary" : "primary"}
    >
      <SearchField.Group>
        <SearchField.SearchIcon />
        <SearchField.Input className="w-full" placeholder={label} />
        <SearchField.ClearButton />
      </SearchField.Group>
    </SearchField>
  );
}
