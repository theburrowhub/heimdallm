/// A GitHub login or org team ("org/slug") offered by the daemon as a
/// never-approve exempt-authors suggestion.
class GitHubPrincipal {
  final String value;
  final bool isTeam;

  const GitHubPrincipal(this.value, {this.isTeam = false});

  factory GitHubPrincipal.fromJson(Map<String, dynamic> json) =>
      GitHubPrincipal(json['value'] as String, isTeam: json['kind'] == 'team');
}
