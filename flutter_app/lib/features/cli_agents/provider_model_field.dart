import 'package:flutter/material.dart';

import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';

/// Required model picker for an in-process provider (OpenRouter).
///
/// A provider has no CLI default: an empty model means the agent cannot
/// review. Its list comes from the provider's API and holds hundreds of ids,
/// so the field searches it as you type. A model id typed by hand is kept on
/// Enter, which also covers a list that could not be loaded.
class ProviderModelField extends StatefulWidget {
  /// Key of the text field itself.
  final Key? fieldKey;

  /// The saved model ('' when none is chosen).
  final String model;

  /// The models the provider offers (empty when the list is unavailable).
  final List<String> models;

  /// Whether [models] is being loaded.
  final bool loading;

  /// Reloads [models]; the Retry button is hidden without it.
  final Future<void> Function()? onRetry;

  final ValueChanged<String> onChanged;

  /// Provider name used in the copy.
  final String providerName;

  const ProviderModelField({
    super.key,
    this.fieldKey,
    required this.model,
    required this.models,
    required this.onChanged,
    this.loading = false,
    this.onRetry,
    this.providerName = 'OpenRouter',
  });

  @override
  State<ProviderModelField> createState() => _ProviderModelFieldState();
}

class _ProviderModelFieldState extends State<ProviderModelField> {
  late final TextEditingController _ctrl = TextEditingController(
    text: widget.model,
  );
  final _focus = FocusNode();
  bool _retrying = false;

  @override
  void initState() {
    super.initState();
    // Leaving the field without picking or pressing Enter drops the search
    // text: a partial query must never become the saved model.
    _focus.addListener(() {
      if (!_focus.hasFocus && _ctrl.text != widget.model) {
        _ctrl.text = widget.model;
      }
    });
  }

  @override
  void didUpdateWidget(ProviderModelField old) {
    super.didUpdateWidget(old);
    if (old.model != widget.model && !_focus.hasFocus) {
      _ctrl.text = widget.model;
    }
  }

  @override
  void dispose() {
    _ctrl.dispose();
    _focus.dispose();
    super.dispose();
  }

  Iterable<String> _matches(String query) {
    final q = query.trim().toLowerCase();
    if (q.isEmpty) return widget.models;
    return widget.models.where((m) => m.toLowerCase().contains(q));
  }

  void _commit(String value) {
    final model = value.trim();
    _ctrl.text = model;
    if (model != widget.model) widget.onChanged(model);
  }

  Future<void> _retry() async {
    final retry = widget.onRetry;
    if (retry == null) return;
    setState(() => _retrying = true);
    try {
      await retry();
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    } finally {
      if (mounted) setState(() => _retrying = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final models = widget.models;
    final model = widget.model.trim();
    final warning = AppColors.warning.resolve(context);
    final name = widget.providerName;

    Widget? status;
    if (models.isEmpty && (widget.loading || _retrying)) {
      status = Row(
        children: [
          const SizedBox(
            width: 14,
            height: 14,
            child: CircularProgressIndicator(strokeWidth: 2),
          ),
          const SizedBox(width: 8),
          AppText.muted('Loading the model list from $name…'),
        ],
      );
    } else if (models.isEmpty) {
      status = Column(
        key: const ValueKey('provider-models-unavailable'),
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(Icons.cloud_off, size: 16, color: warning),
              const SizedBox(width: 8),
              Expanded(child: Text("Couldn't load the model list from $name")),
              if (widget.onRetry != null)
                TextButton.icon(
                  key: const ValueKey('provider-models-retry'),
                  onPressed: _retrying ? null : _retry,
                  icon: const Icon(Icons.refresh, size: 16),
                  label: const Text('Retry'),
                ),
            ],
          ),
          AppText.muted('You can still type a model id and press Enter.'),
        ],
      );
    } else if (model.isNotEmpty && !models.contains(model)) {
      status = Row(
        key: const ValueKey('provider-model-missing'),
        children: [
          Icon(Icons.warning_amber_rounded, size: 16, color: warning),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              '$model is not offered by $name any more. '
              'Choose another model.',
            ),
          ),
        ],
      );
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        LayoutBuilder(
          builder: (context, constraints) => RawAutocomplete<String>(
            textEditingController: _ctrl,
            focusNode: _focus,
            optionsBuilder: (value) => _matches(value.text),
            onSelected: _commit,
            fieldViewBuilder: (context, ctrl, focus, onFieldSubmitted) =>
                TextField(
                  key: widget.fieldKey,
                  controller: ctrl,
                  focusNode: focus,
                  autocorrect: false,
                  enableSuggestions: false,
                  decoration: InputDecoration(
                    labelText: 'Model',
                    hintText: models.isEmpty
                        ? 'Type a model id, e.g. vendor/model'
                        : 'Search ${models.length} models',
                    helperText: 'Required — $name has no default',
                    border: const OutlineInputBorder(),
                    prefixIcon: const Icon(Icons.search, size: 18),
                  ),
                  onSubmitted: (v) {
                    // Enter keeps an exact or unlisted id as typed, and
                    // otherwise picks the highlighted match.
                    if (models.contains(v.trim()) || _matches(v).isEmpty) {
                      _commit(v);
                    } else {
                      onFieldSubmitted();
                    }
                  },
                ),
            optionsViewBuilder: (context, onSelected, options) {
              final list = options.toList(growable: false);
              return Align(
                alignment: Alignment.topLeft,
                child: Material(
                  elevation: 4,
                  borderRadius: BorderRadius.circular(8),
                  child: ConstrainedBox(
                    constraints: BoxConstraints(
                      maxHeight: 280,
                      maxWidth: constraints.maxWidth,
                    ),
                    child: ListView.builder(
                      padding: EdgeInsets.zero,
                      shrinkWrap: true,
                      itemCount: list.length,
                      itemBuilder: (context, i) {
                        final option = list[i];
                        final highlighted =
                            AutocompleteHighlightedOption.of(context) == i;
                        return ListTile(
                          dense: true,
                          selected: highlighted,
                          title: Text(option),
                          onTap: () => onSelected(option),
                        );
                      },
                    ),
                  ),
                ),
              );
            },
          ),
        ),
        if (status != null) ...[const SizedBox(height: 8), status],
      ],
    );
  }
}
